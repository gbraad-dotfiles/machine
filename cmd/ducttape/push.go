package main

import (
	"bytes"
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var pushCommand = &cobra.Command{
	Use:   "push <tag> [registry-reference]",
	Short: "Push a built image to a registry as an OCI artifact",
	Long: `Package a built QCOW2 disk image as an OCI artifact and push it.

  ducttape push fedora-httpd
      → ghcr.io/ducttape-infra/cloud-images/fedora-httpd:latest

  ducttape push myimage ghcr.io/myuser/myimage:tag`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		tag := args[0]

		diskPath := resolveImagePath(tag)
		if diskPath == "" {
			return fmt.Errorf("image %q not found in base images, built images, or as file", tag)
		}

		var ref string
		if len(args) > 1 {
			ref = args[1]
		} else {
			name := strings.TrimSuffix(tag, ":latest")
			ref = fmt.Sprintf("ghcr.io/ducttape-infra/cloud-images/%s:latest", name)
		}

		// Parse reference: strip docker:// prefix if present, split registry/repo:tag
		ref = strings.TrimPrefix(ref, "docker://")
		lastColon := strings.LastIndex(ref, ":")
		var registryRepo, tagName string
		if lastColon >= 0 {
			registryRepo = ref[:lastColon]
			tagName = ref[lastColon+1:]
		} else {
			registryRepo = ref
			tagName = "latest"
		}
		firstSlash := strings.Index(registryRepo, "/")
		if firstSlash < 0 {
			return fmt.Errorf("invalid reference: need registry/repo:tag, got %s", ref)
		}
		registry := registryRepo[:firstSlash]
		repo := registryRepo[firstSlash+1:]

		// Build the OCI image in memory
		diskData, err := os.ReadFile(diskPath)
		if err != nil {
			return fmt.Errorf("read disk: %w", err)
		}

		// Uncompressed digest (for diff_ids in config)
		uncompressedDigest := sha256.Sum256(diskData)
		uncompressedDigestHex := hex.EncodeToString(uncompressedDigest[:])

		// Build tar layer: disk.qcow2 inside a tar, gzip-compressed
		// This matches what podman build produces from a Containerfile.
		var tarBuf bytes.Buffer
		tw := tar.NewWriter(&tarBuf)
		hdr := &tar.Header{
			Name:     "disk.qcow2",
			Size:     int64(len(diskData)),
			Mode:     0644,
			ModTime:  time.Now(),
			Format:   tar.FormatUSTAR,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("tar header: %w", err)
		}
		if _, err := tw.Write(diskData); err != nil {
			return fmt.Errorf("tar data: %w", err)
		}
		tw.Close()

		// Gzip compress the tar
		var compressed bytes.Buffer
		gz := gzip.NewWriter(&compressed)
		if _, err := io.Copy(gz, &tarBuf); err != nil {
			return fmt.Errorf("compress: %w", err)
		}
		gz.Close()
		layerData := compressed.Bytes()
		layerDigest := sha256.Sum256(layerData)
		layerDigestHex := hex.EncodeToString(layerDigest[:])

		// Config blob (diff_ids must be uncompressed digest)
		config := map[string]interface{}{
			"config": map[string]interface{}{},
			"rootfs": map[string]interface{}{
				"type":    "layers",
				"diff_ids": []string{"sha256:" + uncompressedDigestHex},
			},
		}
		configData, _ := json.Marshal(config)
		configDigest := sha256.Sum256(configData)
		configHex := hex.EncodeToString(configDigest[:])

		// Manifest
		manifest := map[string]interface{}{
			"schemaVersion": 2,
			"mediaType":     "application/vnd.docker.distribution.manifest.v2+json",
			"config": map[string]interface{}{
				"mediaType": "application/vnd.docker.container.image.v1+json",
				"size":      len(configData),
				"digest":    "sha256:" + configHex,
			},
			"layers": []map[string]interface{}{
				{
					"mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
					"size":      len(layerData),
					"digest":    "sha256:" + layerDigestHex,
				},
			},
		}
		manifestData, _ := json.Marshal(manifest)

		baseURL := fmt.Sprintf("https://%s/v2/%s", registry, repo)

		// Get auth token
		auth, err := registryAuth(registry, repo)
		if err != nil {
			return fmt.Errorf("auth: %w", err)
		}

		// Upload layer blob
		fmt.Printf("Uploading layer (%d MiB)...\n", len(layerData)>>20)
		if err := uploadBlob(baseURL, auth, layerData, "sha256:"+layerDigestHex); err != nil {
			return fmt.Errorf("upload layer: %w", err)
		}

		// Upload config blob
		if err := uploadBlob(baseURL, auth, configData, "sha256:"+configHex); err != nil {
			return fmt.Errorf("upload config: %w", err)
		}

		// Upload manifest
		req, _ := http.NewRequest("PUT", baseURL+"/manifests/"+tagName, bytes.NewReader(manifestData))
		req.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		req.Header.Set("Authorization", auth)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("manifest push: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("manifest push: %s: %s", resp.Status, string(body))
		}
		fmt.Printf("Pushed %s/%s:%s\n", registry, repo, tagName)
		return nil
	},
}

// registryAuth obtains a Bearer token for the given registry/repo.
func registryAuth(registry, repo string) (string, error) {
	tokenURL := fmt.Sprintf("https://%s/token?scope=repository:%s:pull,push&service=%s", registry, repo, registry)

	user, pass := os.Getenv("REGISTRY_USER"), os.Getenv("REGISTRY_PASSWORD")
	if user == "" || pass == "" {
		cfgPath := os.Getenv("REGISTRY_AUTH_FILE")
		if cfgPath == "" {
			cfgPath = filepath.Join(os.Getenv("HOME"), ".docker", "config.json")
		}
		if data, err := os.ReadFile(cfgPath); err == nil {
			var cfg struct {
				Auths map[string]struct{ Auth string `json:"auth"` } `json:"auths"`
			}
			if json.Unmarshal(data, &cfg) == nil {
				for host, cred := range cfg.Auths {
					if strings.Contains(host, registry) && cred.Auth != "" {
						decoded, _ := base64.StdEncoding.DecodeString(cred.Auth)
						parts := strings.SplitN(string(decoded), ":", 2)
						if len(parts) == 2 {
							user, pass = parts[0], parts[1]
							break
						}
					}
				}
			}
		}
	}

	req, _ := http.NewRequest("GET", tokenURL, nil)
	if user != "" && pass != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	var t struct{ Token string `json:"token"` }
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", fmt.Errorf("parse token: %w", err)
	}
	if t.Token == "" {
		return "", fmt.Errorf("empty token -- check credentials for %s", registry)
	}
	return "Bearer " + t.Token, nil
}

// uploadBlob uploads a blob to the registry using the OCI distribution protocol.
func uploadBlob(baseURL, auth string, data []byte, digest string) error {
	// Start blob upload
	req, _ := http.NewRequest("POST", baseURL+"/blobs/uploads/", nil)
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Length", "0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("start upload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("start upload: %s: %s", resp.Status, string(body))
	}

	// Get upload URL from Location header
	uploadURL := resp.Header.Get("Location")
	if uploadURL == "" {
		return fmt.Errorf("no upload URL in response")
	}
	if strings.HasPrefix(uploadURL, "/") {
		parts := strings.SplitN(baseURL, "/v2", 2)
		uploadURL = parts[0] + uploadURL
	}

	// Upload the blob
	putReq, _ := http.NewRequest("PUT", uploadURL+"?digest="+digest, bytes.NewReader(data))
	putReq.Header.Set("Content-Type", "application/octet-stream")
	putReq.Header.Set("Authorization", auth)
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		return fmt.Errorf("upload blob: %w", err)
	}
	defer putResp.Body.Close()
	if putResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(putResp.Body)
		return fmt.Errorf("upload blob: %s: %s", putResp.Status, string(body))
	}
	return nil
}
