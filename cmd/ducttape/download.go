package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	di "ducttape/pkg/ducttape"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const imageDefaultTag = "latest"

// downloadBaseImage downloads a base image from a URL or OCI registry to the local cache.
// Supported URL schemes:
//
//	file:///path/to/image.qcow2  -- copy from local path
//	https://...                  -- HTTP download
//	registry:ref                 -- pull OCI image, extract .qcow2 layer
//
// Returns the local path to the cached image.
func downloadBaseImage(spec string, cacheName string) (string, error) {
	dest := filepath.Join(baseImagesDir, cacheName+".qcow2")
	_ = os.MkdirAll(baseImagesDir, 0o755)

	// Return cached copy if available
	if fi, err := os.Stat(dest); err == nil && !fi.IsDir() {
		fmt.Printf("  Using cached %s\n", cacheName)
		return dest, nil
	}

	// Dangling symlink: os.Stat fails but os.Create can't write through one either.
	if _, err := os.Lstat(dest); err == nil {
		os.Remove(dest)
	}

	// Dangling symlink: os.Stat fails but os.Create can't write through one either.
	if _, err := os.Lstat(dest); err == nil {
		os.Remove(dest)
	}

	if strings.HasPrefix(spec, "file://") {
		src := strings.TrimPrefix(spec, "file://")
		if err := copyFile(src, dest); err != nil {
			return "", fmt.Errorf("copy %s: %w", src, err)
		}
		return dest, nil
	}

	if strings.HasPrefix(spec, "https://") || strings.HasPrefix(spec, "http://") {
		fmt.Printf("Downloading %s ...\n", spec)
		out, err := os.Create(dest)
		if err != nil {
			return "", fmt.Errorf("create %s: %w", dest, err)
		}
		defer out.Close()
		resp, err := http.Get(spec)
		if err != nil {
			return "", fmt.Errorf("http get %s: %w", spec, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("http %s: %s", spec, resp.Status)
		}
		total := resp.ContentLength
		prog := &progressWriter{total: total}
		if _, err := io.Copy(io.MultiWriter(out, prog), resp.Body); err != nil {
			return "", fmt.Errorf("download %s: %w", spec, err)
		}
		fmt.Println()
		return dest, nil
	}

	if strings.HasPrefix(spec, "registry:") {
		ref := strings.TrimPrefix(spec, "registry:")
		return pullFromRegistry(ref, dest)
	}

	// Bare registry reference (e.g. ghcr.io/org/repo:tag) -- treat as OCI pull
	return pullFromRegistry(spec, dest)
}

// pullFromRegistry pulls an OCI image from a registry, extracts the disk image
// from its layers, and saves it to dest.
func pullFromRegistry(ref string, dest string) (string, error) {
	fmt.Printf("Pulling OCI image %s ...\n", ref)

	parts := strings.SplitN(ref, "/", 2)
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid registry reference (need registry/repo:tag): %s", ref)
	}
	registry := parts[0]
	repo := parts[1]
	tag := "latest"
	if colon := strings.LastIndex(repo, ":"); colon >= 0 {
		tag = repo[colon+1:]
		repo = repo[:colon]
	}

	baseURL := fmt.Sprintf("https://%s/v2/%s", registry, repo)
	tokenURL := fmt.Sprintf("https://%s/token?scope=repository:%s:pull&service=%s", registry, repo, registry)

	tokenResp, err := http.Get(tokenURL)
	if err != nil {
		return "", fmt.Errorf("auth token: %w", err)
	}
	defer tokenResp.Body.Close()
	var tokenData struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenData); err != nil {
		return "", fmt.Errorf("parse token: %w", err)
	}
	auth := "Bearer " + tokenData.Token

	req, _ := http.NewRequest("GET", baseURL+"/manifests/"+tag, nil)
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
	req.Header.Set("Authorization", auth)
	manResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("manifest: %w", err)
	}
	defer manResp.Body.Close()

	if manResp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("tag %q not found on registry", tag)
	}
	if manResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(manResp.Body)
		return "", fmt.Errorf("manifest: %s: %s", manResp.Status, string(body))
	}

	var manifest struct {
		Layers []struct {
			Digest    string `json:"digest"`
			MediaType string `json:"mediaType"`
		} `json:"layers"`
	}
	if err := json.NewDecoder(manResp.Body).Decode(&manifest); err != nil {
		return "", fmt.Errorf("parse manifest: %w", err)
	}
	if len(manifest.Layers) == 0 {
		return "", fmt.Errorf("no layers in manifest")
	}

	if skopeoPath, err := exec.LookPath("skopeo"); err == nil && skopeoPath != "" {
		tmpDir, _ := os.MkdirTemp("", "ducttape-pull-*")
		defer os.RemoveAll(tmpDir)
		dlURL := fmt.Sprintf("docker://%s/%s:%s", registry, repo, tag)
		fmt.Printf("  Downloading %s ...\n", dlURL)
		dirDest := fmt.Sprintf("dir:%s", tmpDir)
		cmd := exec.Command(skopeoPath, "copy", "--src-no-creds", dlURL, dirDest)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("skopeo pull: %w", err)
		}
		fmt.Print("  Extracting QCOW2 ... ")
		res, err := extractQCOWFromDir(tmpDir, manifest.Layers[0].Digest, dest)
		if err == nil {
			fmt.Println("done.")
		}
		return res, err
	}

	tmpDir, _ := os.MkdirTemp("", "ducttape-pull-*")
	defer os.RemoveAll(tmpDir)

	for i, layer := range manifest.Layers {
		fmt.Printf("  downloading layer %d/%d (%s)...\n", i+1, len(manifest.Layers), layer.Digest[:16])
		req, _ := http.NewRequest("GET", baseURL+"/blobs/"+layer.Digest, nil)
		req.Header.Set("Authorization", auth)
		blobResp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("layer %d: %w", i, err)
		}
		layerFile := filepath.Join(tmpDir, fmt.Sprintf("layer-%d", i))
		f, _ := os.Create(layerFile)
		pw := &progressWriter{total: blobResp.ContentLength}
		io.Copy(io.MultiWriter(f, pw), blobResp.Body)
		f.Close()
		blobResp.Body.Close()
	}

	for i := range manifest.Layers {
		layerFile := filepath.Join(tmpDir, fmt.Sprintf("layer-%d", i))
		if found := extractQCOWFromTar(layerFile, dest); found {
			return dest, nil
		}
	}

	return "", fmt.Errorf("no .qcow2 found in any layer")
}

// extractQCOWFromDir finds a blob file in a skopeo dir pull and
// extracts any .qcow2 found.
func extractQCOWFromDir(dir string, digest string, dest string) (string, error) {
	hex := strings.TrimPrefix(digest, "sha256:")
	blobPath := filepath.Join(dir, "blobs", "sha256", hex)
	if fi, err := os.Stat(blobPath); err == nil && !fi.IsDir() {
		if found := extractQCOWFromTar(blobPath, dest); found {
			return dest, nil
		}
		if err := copyFile(blobPath, dest); err == nil {
			return dest, nil
		}
	}
	filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".qcow2") {
			copyFile(path, dest)
			return filepath.SkipAll
		}
		if extractQCOWFromTar(path, dest) {
			return filepath.SkipAll
		}
		return nil
	})
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	return "", fmt.Errorf("could not extract qcow2 from pull output")
}

// extractQCOWFromTar opens path, decompresses if gzipped, reads tar entries,
// and extracts any .qcow2 file found to dest.
func extractQCOWFromTar(path string, dest string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	var tr *tar.Reader
	if gz, err := gzip.NewReader(f); err == nil {
		tr = tar.NewReader(gz)
		defer gz.Close()
	} else {
		f.Seek(0, 0)
		tr = tar.NewReader(f)
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false
		}
		if !strings.HasSuffix(hdr.Name, ".qcow2") {
			continue
		}
		out, err := os.Create(dest)
		if err != nil {
			return false
		}
		defer out.Close()
		if _, err := io.Copy(out, tr); err != nil {
			return false
		}
		return true
	}
	return false
}

// resolveBaseImage resolves a base image spec to a local QCOW2 file path.
// Order:
//  1. If spec is an existing file, use it directly.
//  2. If spec matches a cached image in baseImagesDir, use it.
//  3. If spec is a known alias, download and cache from the configured registry.
//  4. Otherwise error.
// progressWriter prints download progress every 10 MiB.
type progressWriter struct {
	total   int64
	written int64
	next    int64
}

func (w *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.written += int64(n)
	if w.written >= w.next {
		w.next = w.written + 10<<20 // 10 MiB
		pct := ""
		if w.total > 0 {
			pct = fmt.Sprintf(" (%.0f%%)", float64(w.written)/float64(w.total)*100)
		}
		fmt.Printf("\r  %s / %s%s",
			humanSize(w.written), humanSize(w.total), pct)
	}
	return n, nil
}

func humanSize(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/float64(1<<20))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func resolveBaseImage(spec string) (string, error) {
	// 1. Direct file path
	for _, p := range []string{spec, spec + ".qcow2"} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	// 2. registry: URL
	if strings.HasPrefix(spec, "registry:") {
		return downloadBaseImage(spec, filepath.Base(spec))
	}
	// 3. Cached in baseImagesDir or imagesDir
	for _, dir := range []string{baseImagesDir, imagesDir} {
		for _, name := range []string{spec, spec + ":" + imageDefaultTag} {
			candidate := filepath.Join(dir, name+".qcow2")
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
	}
	// 4. Known alias (with tag support: fedora-cloud:42 -> alias + tag)
	baseName, tag := spec, ""
	if colon := strings.LastIndex(spec, ":"); colon >= 0 && !strings.Contains(spec[colon:], "/") {
		baseName = spec[:colon]
		tag = spec[colon:]
	}
	if url, ok := di.KnownAliases[baseName]; ok {
		fmt.Printf("Resolved alias %q\n", spec)
		return downloadBaseImage(url+tag, baseName)
	}
	// 5. Full registry reference (e.g. ghcr.io/org/repo:tag) — pass through
	if isFullRegistryRef(spec) {
		return downloadBaseImage("registry:"+spec, spec)
	}
	// 6. Default to registry lookup (short name like fedora-cloud)
	fmt.Printf("Looking up %q from registry...\n", spec)
	if tag == "" {
		tag = ":latest"
	}
	return downloadBaseImage("registry:ghcr.io/ducttape-infra/cloud-images/"+baseName+tag, baseName)
}

// isFullRegistryRef reports whether spec looks like a full registry reference
// (host[:port]/path) as opposed to a short alias name.
func isFullRegistryRef(spec string) bool {
	parts := strings.SplitN(spec, "/", 2)
	if len(parts) < 2 {
		return false
	}
	// The first component of a full ref is a registry host containing a dot or colon.
	return strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":")
}

