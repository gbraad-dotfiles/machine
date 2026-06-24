package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var pushCommand = &cobra.Command{
	Use:   "push <tag> [registry-reference]",
	Short: "Push a built image to a registry as an OCI artifact",
	Long: `Package a built QCOW2 disk image as an OCI artifact and push it.

  machine push fedora-httpd
      → ghcr.io/gbraad-dotfiles/fedora-httpd-disk:latest

  machine push myimage docker://ghcr.io/myuser/myimage:tag
      → custom registry reference`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		tag := args[0]

		diskPath := filepath.Join(imagesDir, tag+".qcow2")
		if fi, err := os.Stat(diskPath); err != nil || fi.IsDir() {
			diskPath = tag
			if fi, err := os.Stat(diskPath); err != nil || fi.IsDir() {
				return fmt.Errorf("image %q not found in %s or as file", tag, imagesDir)
			}
		}

		skopeo, err := exec.LookPath("skopeo")
		if err != nil {
			return fmt.Errorf("skopeo not found; install skopeo to push images")
		}

		var ref string
		if len(args) > 1 {
			ref = args[1]
		} else {
			name := strings.TrimSuffix(tag, ":latest")
			ref = fmt.Sprintf("docker://ghcr.io/gbraad-dotfiles/%s-disk:latest", name)
		}

		tmpDir, err := os.MkdirTemp("", "machine-push-*")
		if err != nil {
			return fmt.Errorf("create temp dir: %w", err)
		}
		defer os.RemoveAll(tmpDir)

		// Create OCI layout manually: the QCOW2 is stored as a gzip-compressed
		// layer blob.  Build a minimal manifest and use skopeo to push.
		// Compress the QCOW2 as gzip layer
		blobDir := filepath.Join(tmpDir, "blobs", "sha256")
		os.MkdirAll(blobDir, 0o755)
		layerFile := filepath.Join(blobDir, "layer.gz")
		{
			src, err := os.Open(diskPath)
			if err != nil {
				return err
			}
			defer src.Close()
			out, err := os.Create(layerFile)
			if err != nil {
				return err
			}
			defer out.Close()
			gz := gzip.NewWriter(out)
			if _, err := io.Copy(gz, src); err != nil {
				return fmt.Errorf("compress layer: %w", err)
			}
			gz.Close()
		}

		// Compute digest
		layerData, _ := os.ReadFile(layerFile)
		digest := sha256.Sum256(layerData)
		digestHex := hex.EncodeToString(digest[:])
		layerDigest := "sha256:" + digestHex

		// Move blob to digest-named file
		blobPath := filepath.Join(blobDir, digestHex)
		os.Rename(layerFile, blobPath)

		// Minimal OCI config
		config := map[string]interface{}{
			"config": map[string]interface{}{},
			"rootfs": map[string]interface{}{
				"type":    "layers",
				"diff_ids": []string{layerDigest},
			},
		}
		configData, _ := json.Marshal(config)
		configDigest := sha256.Sum256(configData)
		configHex := hex.EncodeToString(configDigest[:])
		os.WriteFile(filepath.Join(blobDir, configHex), configData, 0o644)

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
					"digest":    layerDigest,
				},
			},
		}
		manifestData, _ := json.Marshal(manifest)
		manifestDigest := sha256.Sum256(manifestData)
		manifestHex := hex.EncodeToString(manifestDigest[:])
		os.WriteFile(filepath.Join(blobDir, manifestHex), manifestData, 0o644)

		// Write index.json
		index := map[string]interface{}{
			"schemaVersion": 2,
			"manifests": []map[string]interface{}{
				{
					"mediaType": "application/vnd.docker.distribution.manifest.v2+json",
					"size":      len(manifestData),
					"digest":    "sha256:" + manifestHex,
					"annotations": map[string]string{
						"org.opencontainers.image.ref.name": "latest",
					},
				},
			},
		}
		indexData, _ := json.Marshal(index)
		os.WriteFile(filepath.Join(tmpDir, "index.json"), indexData, 0o644)

		// Push via skopeo (suppress its output)
		fmt.Printf("Pushing %s ...\n", ref)
		push := exec.Command(skopeo, "copy", fmt.Sprintf("oci:%s:latest", tmpDir), ref)
		if err := push.Run(); err != nil {
			return fmt.Errorf("push failed: %w", err)
		}
		fmt.Println("Done.")
		return nil
	},
}

