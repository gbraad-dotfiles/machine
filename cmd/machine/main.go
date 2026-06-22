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
)

const (
	machineHome   = "/home/gbraad/.local/share/machine"
	baseImagesDir = machineHome + "/base-images"
	imagesDir     = machineHome + "/images"
	configDir     = "/home/gbraad/.config/containers/macadam/machine/qemu"
)

type VMInfo struct {
	Name       string
	SSHPort    int
	SSHUser    string
	SSHKeyPath string
}

func init() {
	rand.Seed(time.Now().UnixNano())
}

// random string for temporary VM name
func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

// ensure directories exist
func ensureDirs() {
	os.MkdirAll(baseImagesDir, 0o755)
	os.MkdirAll(imagesDir, 0o755)
}

// resolve base image: if spec points to an existing file, use it; otherwise treat as a tag and look in baseImagesDir
func resolveBaseImage(spec string) (string, error) {
	if fi, err := os.Stat(spec); err == nil && !fi.IsDir() {
		return spec, nil
	}
	// assume it's a tag, look for <tag>.qcow2 in baseImagesDir
	candidate := filepath.Join(baseImagesDir, spec+".qcow2")
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	return "", fmt.Errorf("base image %q not found (as file) or in %s", spec, baseImagesDir)
}

// read SSH info from macadam config JSON
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
	// expand ~ in IdentityPath
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

// run a command and return its combined output
func runCmd(name string, arg ...string) error {
	cmd := exec.Command(name, arg...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// wait for SSH to be ready
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

// build subcommand
func buildCmd(tag string, machinefile string, baseSpec string) error {
	ensureDirs()

	// resolve base image
	basePath, err := resolveBaseImage(baseSpec)
	if err != nil {
		return err
	}

	// create temporary VM name
	tmpName := "machine-build-" + randomString(6)
	defer func() {
		// best effort cleanup
		runCmd("macadam", "rm", "-f", tmpName)
	}()

	// init VM with base image (using defaults from dotini? we'll use some defaults)
	// We'll need to get defaults from dotini; for now use hardcoded or read from env.
	// For simplicity, we'll use defaults: 2 CPUs, 2048 MB memory, 10GB disk, user fedora.
	if err := runCmd("macadam", "init",
		"--name", tmpName,
		"--cpus", "2",
		"--memory", "2048",
		"--disk-size", "10",
		"--username", "fedora",
		basePath); err != nil {
		return fmt.Errorf("macadam init failed: %w", err)
	}

	// start VM
	if err := runCmd("macadam", "start", tmpName); err != nil {
		return fmt.Errorf("macadam start failed: %w", err)
	}
	// ensure stop on error
	defer func() {
		runCmd("macadam", "stop", tmpName)
	}()

	// wait for SSH
	info, err := readSSHInfo(tmpName)
	if err != nil {
		return err
	}
	if err := waitForSSH(info, 2*time.Minute); err != nil {
		return err
	}

	// run machinefile executor against the VM
	// We'll use the machinefile CLI: machinefile --host localhost --port <port> --user <user> --key <key> <machinefile> .
	// The context is current directory.
	args := []string{
		"machinefile",
		"--host", "localhost",
		"--port", strconv.Itoa(info.SSHPort),
		"--user", info.SSHUser,
		"--key", info.SSHKeyPath,
		machinefile,
		".",
	}
	if err := runCmd(args[0], args[1:]...); err != nil {
		return fmt.Errorf("machinefile execution failed: %w", err)
	}

	// stop VM before copying disk
	if err := runCmd("macadam", "stop", tmpName); err != nil {
		return fmt.Errorf("macadam stop failed: %w", err)
	}

	// copy disk to images store: we need source disk path from config
	path := filepath.Join(configDir, tmpName+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var config struct {
		ImagePath struct {
			Path string `json:"Path"`
		} `json:"ImagePath"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return err
	}
	sourceDisk := config.ImagePath.Path
	if sourceDisk == "" {
		return fmt.Errorf("could not determine disk path from config")
	}
	destDisk := filepath.Join(imagesDir, tag+".qcow2")
	if err := copyFile(sourceDisk, destDisk); err != nil {
		return fmt.Errorf("failed to copy disk: %w", err)
	}

	fmt.Printf("Built image %s saved to %s\n", tag, destDisk)
	return nil
}

// copyFile copies a file from src to dst
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

// images subcommand: list base and built images
func imagesCmd() {
	fmt.Println("Base images:")
	files, err := os.ReadDir(baseImagesDir)
	if err != nil {
		fmt.Printf("  (error reading %s: %v)\n", baseImagesDir, err)
	} else {
		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".qcow2") {
				tag := strings.TrimSuffix(f.Name(), ".qcow2")
				fmt.Printf("  %s\n", tag)
			}
		}
	}
	fmt.Println("Built images:")
	files, err = os.ReadDir(imagesDir)
	if err != nil {
		fmt.Printf("  (error reading %s: %v)\n", imagesDir, err)
	} else {
		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".qcow2") {
				tag := strings.TrimSuffix(f.Name(), ".qcow2")
				fmt.Printf("  %s\n", tag)
			}
		}
	}
}

// ps subcommand: list running VMs via macadam list
func psCmd() {
	fmt.Println("Running VMs:")
	if err := runCmd("macadam", "list"); err != nil {
		fmt.Printf("  (error running macadam list: %v)\n", err)
	}
}

// stop subcommand stub
func stopCmd(vmName string) {
	fmt.Printf("Stopping VM %s (not yet implemented)\n", vmName)
	// TODO: implement using macadam stop
}

// rm subcommand stub
func rmCmd(vmName string) {
	fmt.Printf("Removing VM %s (not yet implemented)\n", vmName)
	// TODO: implement using macadam rm -f
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: machine <command> [args]\n")
		os.Exit(1)
	}
	command := os.Args[1]
	switch command {
	case "build":
		// parse flags
		tag := ""
		machinefile := "Machinefile"
		baseSpec := ""
		for i := 2; i < len(os.Args); i++ {
			arg := os.Args[i]
			if arg == "-t" && i+1 < len(os.Args) {
				tag = os.Args[i+1]
				i++
			} else if arg == "-f" && i+1 < len(os.Args) {
				machinefile = os.Args[i+1]
				i++
			} else if arg == "-d" && i+1 < len(os.Args) {
				baseSpec = os.Args[i+1]
				i++
			} else if arg == "--help" {
				fmt.Println("Usage: machine build -t <tag> -f <Machinefile> -d <base-image>")
				os.Exit(0)
			} else {
				fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", arg)
				os.Exit(1)
			}
		}
		if tag == "" || baseSpec == "" {
			fmt.Fprintf(os.Stderr, "Missing required flags: -t <tag> -d <base-image>\n")
			os.Exit(1)
		}
		if err := buildCmd(tag, machinefile, baseSpec); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "run":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: machine run <image> [--name <vm>] [--cpus N] [--memory M] [--disk-size G]\n")
			os.Exit(1)
		}
		image := os.Args[2]
		fmt.Printf("run command not yet implemented for image %s\n", image)
	case "shell":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: machine shell <vm-or-tag>\n")
			os.Exit(1)
		}
		vm := os.Args[2]
		fmt.Printf("shell command not yet implemented for %s\n", vm)
	case "images":
		imagesCmd()
	case "ps":
		psCmd()
	case "stop":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: machine stop <vm>\n")
			os.Exit(1)
		}
		vm := os.Args[2]
		stopCmd(vm)
	case "rm":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: machine rm <vm>\n")
			os.Exit(1)
		}
		vm := os.Args[2]
		rmCmd(vm)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		os.Exit(1)
	}
}
