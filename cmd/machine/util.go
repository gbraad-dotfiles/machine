package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// findBinary searches for an executable binary.
// order: envVar if set, exec.LookPath, ./bin/<name> relative to executable, $HOME/.cache/machine/bin/<name>.
func findBinary(name string, envVar string) (string, error) {
	if v := os.Getenv(envVar); v != "" {
		if fi, err := os.Stat(v); err == nil && fi.Mode()&0111 != 0 {
			return v, nil
		}
		return "", fmt.Errorf("%s set to %q but not executable or not found", envVar, v)
	}
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		candidate := filepath.Join(dir, "bin", name)
		if fi, err := os.Stat(candidate); err == nil && fi.Mode()&0111 != 0 {
			return candidate, nil
		}
	}
	home := os.Getenv("HOME")
	if home != "" {
		candidate := filepath.Join(home, ".cache", "machine", "bin", name)
		if fi, err := os.Stat(candidate); err == nil && fi.Mode()&0111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s not found in PATH, ./bin, or $HOME/.cache/machine/bin", name)
}

// setupEnv prepares the runtime environment so that the macadam library
// can find helper binaries (gvproxy).  Macadam is used as a Go library
// only — no macadam binary lookup.
func setupEnv(cmd *cobra.Command) func() {
	addToPath := func(dir string) {
		path := os.Getenv("PATH")
		for _, p := range filepath.SplitList(path) {
			if p == dir {
				return
			}
		}
		os.Setenv("PATH", dir+string(filepath.ListSeparator)+path)
	}
	// Extract the embedded gvproxy binary and make it discoverable.
	if p, err := extractEmbeddedGVProxy(); err == nil {
		gvproxyPath = p
		dir := filepath.Dir(p)
		addToPath(dir)
		os.Setenv("CONTAINERS_HELPER_BINARY_DIR", dir)
	}
	if limaPath, err := findBinary("lima", "LIMA_BIN"); err == nil {
		addToPath(filepath.Dir(limaPath))
	}
	return func() {}
}

// randomString returns a random alphanumeric string of length n.
func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

// ensureDirs creates the base images and built images directories.
func ensureDirs() {
	os.MkdirAll(baseImagesDir, 0o755)
	os.MkdirAll(imagesDir, 0o755)
}

// readSSHInfo reads the macadam config JSON for a VM and returns its SSH info.
func readSSHInfo(vmName string) (*VMInfo, error) {
	path := filepath.Join(configDir, vmName+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config %s: %w", path, err)
	}
	var config struct {
		SSH struct {
			Port             int    `json:"Port"`
			RemoteUsername   string `json:"RemoteUsername"`
			IdentityPath     string `json:"IdentityPath"`
		} `json:"SSH"`
		ImagePath struct {
			Path string `json:"Path"`
		} `json:"ImagePath"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	identity := config.SSH.IdentityPath
	if strings.HasPrefix(identity, "~/") {
		identity = filepath.Join(os.Getenv("HOME"), identity[2:])
	}
	return &VMInfo{
		Name:       vmName,
		SSHPort:    config.SSH.Port,
		SSHUser:    config.SSH.RemoteUsername,
		SSHKeyPath: identity,
	}, nil
}

// runCmd runs a command, connecting its stdout/stderr to the terminal.
func runCmd(name string, arg ...string) error {
	cmd := exec.Command(name, arg...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// waitForSSH polls SSH connectivity until the VM is ready or a timeout expires.
func waitForSSH(info *VMInfo, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.Command("ssh", "-i", info.SSHKeyPath, "-p", strconv.Itoa(info.SSHPort),
			"-o", "StrictHostKeyChecking=no", "-o", "BatchMode=yes",
			fmt.Sprintf("%s@localhost", info.SSHUser), "true")
		if err := cmd.Run(); err == nil {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timeout waiting for SSH")
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// vmIsRunning checks whether a QEMU process for the given VM name exists.
func vmIsRunning(name string) bool {
	out, err := exec.Command("pgrep", "-f", "qemu.*"+name).Output()
	return err == nil && len(out) > 0
}

// reapZombies reaps any zombie child processes.  Macadam starts QEMU/gvproxy
// as child processes; if they crash they can become zombies and cause
// shim.Stop / shim.Start to hang waiting for a PID that never dies.
// waitForSSHRoot polls SSH connectivity to localhost:port as root with
// password auth.  Built images have the fedora user removed and only
// allow root SSH with the password set during the pre-Machinefile phase.
func waitForSSHRoot(port int, password string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.Command("sshpass", "-p", password, "ssh",
			"-o", "StrictHostKeyChecking=no",
			"-o", "BatchMode=yes",
			"-p", strconv.Itoa(port),
			fmt.Sprintf("root@localhost"), "true")
		if err := cmd.Run(); err == nil {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timeout waiting for SSH")
}

// readQEMUPid reads the QEMU PID from the VM's pidfile in the macadam
// config directory.  Returns 0 if the file doesn't exist or can't be read.
func readQEMUPid(vmName string) int {
	pidPath := filepath.Join(os.Getenv("HOME"), ".local", "share", "containers", "podman", "machine", "qemu")
	// Try the actual QEMU pid path from the config directory
	pidFile := filepath.Join(pidPath, vmName+"_vm.pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		// Try old-style path
		pidFile = filepath.Join("/run/user", os.Getenv("UID"), "podman", vmName+"_vm.pid")
		data, err = os.ReadFile(pidFile)
		if err != nil {
			return 0
		}
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}
