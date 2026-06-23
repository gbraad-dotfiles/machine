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
	"github.com/spf13/viper"
)
import _ "embed"
//go:embed assets/gvproxy
var gvproxyData []byte
//go:embed assets/macadam
var macadamData []byte

var (
	machineHome   = "/home/gbraad/.local/share/machine"
	baseImagesDir = machineHome + "/base-images"
	imagesDir     = machineHome + "/images"
	configDir     = "/home/gbraad/.config/containers/macadam/machine/qemu"
)

var macadamBin string
// setupEnv returns a func that sets up the environment for macadam/gvproxy.
// It caches the binaries in $HOME/.cache/machine/bin to avoid re-extraction.
// It sets CONTAINERS_HELPER_BINARY_DIR so gvproxy is found without containers.conf.
// It sets the package variable macadamBin to the path of the macadam binary.
// The returned func does nothing (cleanup is not needed).
func setupEnv(cmd *cobra.Command) func() {
    cacheDir := filepath.Join(os.Getenv("HOME"), ".cache", "machine", "bin")
    if err := os.MkdirAll(cacheDir, 0o755); err != nil {
        fmt.Fprintf(cmd.OutOrStderr(), "Failed to create cache dir: %v\n", err)
        os.Exit(1)
    }
    gvproxyPath := filepath.Join(cacheDir, "gvproxy")
    macadamPath := filepath.Join(cacheDir, "macadam")
    // Check if both binaries exist and are executable
    if _, err1 := os.Stat(gvproxyPath); err1 == nil {
        if _, err2 := os.Stat(macadamPath); err2 == nil {
            // both exist, use them
            if err := os.Setenv("CONTAINERS_HELPER_BINARY_DIR", cacheDir); err != nil {
                fmt.Fprintf(cmd.OutOrStderr(), "Failed to set CONTAINERS_HELPER_BINARY_DIR: %v\n", err)
                os.Exit(1)
            }
            macadamBin = macadamPath
            return func() {} // no cleanup
        }
    }
    // otherwise, extract to cache
    if err := os.WriteFile(gvproxyPath, gvproxyData, 0o755); err != nil {
        fmt.Fprintf(cmd.OutOrStderr(), "Failed to write gvproxy binary: %v\n", err)
        os.Exit(1)
    }
    if err := os.WriteFile(macadamPath, macadamData, 0o755); err != nil {
        fmt.Fprintf(cmd.OutOrStderr(), "Failed to write macadam binary: %v\n", err)
        os.Exit(1)
    }
    if err := os.Setenv("CONTAINERS_HELPER_BINARY_DIR", cacheDir); err != nil {
        fmt.Fprintf(cmd.OutOrStderr(), "Failed to set CONTAINERS_HELPER_BINARY_DIR: %v\n", err)
        os.Exit(1)
    }
    macadamBin = macadamPath
    return func() {} // no cleanup, keep binaries in cache
}

type VMInfo struct {
	Name       string
	SSHPort    int
	SSHUser    string
	SSHKeyPath string
}

func init() {
	// Initialize Viper configuration.
	v := viper.New()
	v.SetConfigName("machine")
	v.AddConfigPath("$HOME/.config")
	v.AddConfigPath(".")
	// Attempt to read config; ignore error if not found.
	if err := v.ReadInConfig(); err == nil {
		// Config file found; override defaults if provided.
		if home := v.GetString("machine_home"); home != "" {
			machineHome = home
		}
		if base := v.GetString("base_images_dir"); base != "" {
			baseImagesDir = base
		}
		if images := v.GetString("images_dir"); images != "" {
			imagesDir = images
		}
		if cfg := v.GetString("config_dir"); cfg != "" {
			configDir = cfg
		}
	}
	// Seed random number generator.
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

// buildCommand represents the build subcommand
var buildCommand = &cobra.Command{
	Use:   "build",
	Short: "Build a new image from a base image and a Machinefile",
	Run: func(cmd *cobra.Command, args []string) {
		tag, _ := cmd.Flags().GetString("tag")
		machinefile, _ := cmd.Flags().GetString("file")
		baseSpec, _ := cmd.Flags().GetString("base")
		if tag == "" || baseSpec == "" {
			fmt.Fprintln(cmd.OutOrStderr(), "Error: --tag and --base are required")
			os.Exit(1)
		}
		ensureDirs()

		cleanup := setupEnv(cmd)
		defer cleanup()

		// resolve base image
		basePath, err := resolveBaseImage(baseSpec)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "Error resolving base image: %v\n", err)
			os.Exit(1)
		}

		// create temporary VM name
		tmpName := "machine-build-" + randomString(6)
		// cleanup function for VM
		vmCleanup := func() {
			// best effort cleanup
			runCmd(macadamBin, "rm", "-f", tmpName)
		}
		defer vmCleanup()
		defer cleanup()

		// init VM with base image (defaults)
		if err := runCmd(macadamBin, "init",
			"--name", tmpName,
			"--cpus", "2",
			"--memory", "2048",
			"--disk-size", "10",
			"--username", "fedora",
			basePath); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "macadam init failed: %v\n", err)
			os.Exit(1)
		}

		// start VM
		if err := runCmd(macadamBin, "start", tmpName); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "macadam start failed: %v\n", err)
			os.Exit(1)
		}
		// ensure stop on error
		defer func() {
			runCmd(macadamBin, "stop", tmpName)
		}()

		// wait for SSH
		info, err := readSSHInfo(tmpName)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "failed to read SSH info: %v\n", err)
			os.Exit(1)
		}
		if err := waitForSSH(info, 2*time.Minute); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "timeout waiting for SSH: %v\n", err)
			os.Exit(1)
		}

		// run machinefile executor against the VM
		machinefileCmd := "machinefile"
		if _, err := exec.LookPath("machinefile"); err != nil {
			// fallback to go run in the machinefile repo
			machinefileCmd = "go"
			args := []string{"run", "./cmd/machinefile",
				"--host", "localhost",
				"--port", strconv.Itoa(info.SSHPort),
				"--user", info.SSHUser,
				"--key", info.SSHKeyPath,
				machinefile,
				".",
			}
			ecmd := exec.Command(machinefileCmd, args...)
			ecmd.Stdout = os.Stdout
			ecmd.Stderr = os.Stderr
			if err := ecmd.Run(); err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "machinefile execution failed: %v\n", err)
				os.Exit(1)
			}
		} else {
			args := []string{
				"--host", "localhost",
				"--port", strconv.Itoa(info.SSHPort),
				"--user", info.SSHUser,
				"--key", info.SSHKeyPath,
				machinefile,
				".",
			}
			ecmd := exec.Command(machinefileCmd, args...)
			ecmd.Stdout = os.Stdout
			ecmd.Stderr = os.Stderr
			if err := ecmd.Run(); err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "machinefile execution failed: %v\n", err)
				os.Exit(1)
			}
		}

		// stop VM before copying disk
		if err := runCmd(macadamBin, "stop", tmpName); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "macadam stop failed: %v\n", err)
			os.Exit(1)
		}

		// copy disk to images store: we need source disk path from config
		path := filepath.Join(configDir, tmpName+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "failed to read config after stop: %v\n", err)
			os.Exit(1)
		}
		var config struct {
			ImagePath struct {
				Path string `json:"Path"`
			} `json:"ImagePath"`
		}
		if err := json.Unmarshal(data, &config); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "failed to parse config: %v\n", err)
			os.Exit(1)
		}
		sourceDisk := config.ImagePath.Path
		if sourceDisk == "" {
			fmt.Fprintf(cmd.OutOrStderr(), "could not determine disk path from config\n")
			os.Exit(1)
		}
		destDisk := filepath.Join(imagesDir, tag+".qcow2")
		if err := copyFile(sourceDisk, destDisk); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "failed to copy disk: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Built image %s saved to %s\n", tag, destDisk)
	},
}

// runCommand represents the run subcommand
var runCommand = &cobra.Command{
	Use:   "run <image>",
	Short: "Run a VM from an image",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) < 1 {
			fmt.Fprintln(cmd.OutOrStderr(), "Error: image argument required")
			os.Exit(1)
		}
		image := args[0]
		// TODO: implement using macadam init/start
		fmt.Printf("run command not yet fully implemented for image %s\n", image)
	},
}

// shellCommand represents the shell subcommand
var shellCommand = &cobra.Command{
	Use:   "shell <vm-or-tag>",
	Short: "Open an SSH shell into a running VM",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) < 1 {
			fmt.Fprintln(cmd.OutOrStderr(), "Error: vm-or-tag argument required")
			os.Exit(1)
		}
		vm := args[0]
		fmt.Printf("shell command not yet fully implemented for %s\n", vm)
		// TODO: implement using macadam ssh or retrieve SSH info and exec ssh
	},
}

// imagesCommand lists images
var imagesCommand = &cobra.Command{
	Use:   "images",
	Short: "List base and built images",
	Run: func(cmd *cobra.Command, args []string) {
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
	},
}

// psCommand lists running VMs
var psCommand = &cobra.Command{
	Use:   "ps",
	Short: "List running VMs",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Running VMs:")
		cleanup := setupEnv(cmd)
		defer cleanup()
		if err := runCmd(macadamBin, "list"); err != nil {
			fmt.Printf("  (error running macadam list: %v)\n", err)
		}
	},
}

// stopCommand stops a VM
var stopCommand = &cobra.Command{
	Use:   "stop <vm>",
	Short: "Stop a running VM",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) < 1 {
			fmt.Fprintln(cmd.OutOrStderr(), "Error: vm argument required")
			os.Exit(1)
		}
		vm := args[0]
		cleanup := setupEnv(cmd)
		defer cleanup()
		if err := runCmd(macadamBin, "stop", vm); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "error stopping VM %s: %v\n", vm, err)
			os.Exit(1)
		}
		fmt.Printf("Stopped VM %s\n", vm)
	},
}

// rmCommand removes a VM
var rmCommand = &cobra.Command{
	Use:   "rm <vm>",
	Short: "Remove a VM",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) < 1 {
			fmt.Fprintln(cmd.OutOrStderr(), "Error: vm argument required")
			os.Exit(1)
		}
		vm := args[0]
		cleanup := setupEnv(cmd)
		defer cleanup()
		if err := runCmd(macadamBin, "rm", "-f", vm); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "error removing VM %s: %v\n", vm, err)
			os.Exit(1)
		}
		fmt.Printf("Removed VM %s\n", vm)
	},
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "machine",
		Short: "Machine CLI for managing cloud-init based VM images",
		Long:  `Machine provides a Docker-like workflow for building and running VM images using Machinefiles.`,
	}

	// Build command flags
	buildCommand.Flags().StringP("tag", "t", "", "Tag name for the resulting image")
	buildCommand.Flags().StringP("file", "f", "", "Path to the Machinefile")
	buildCommand.Flags().StringP("base", "d", "", "Base image (path or tag)")

	// Run command flags
	runCommand.Flags().StringP("name", "n", "", "Name for the VM (optional)")
	runCommand.Flags().StringP("cpus", "c", "", "Number of CPUs")
	runCommand.Flags().StringP("memory", "m", "", "Memory in MB")
	runCommand.Flags().StringP("disk-size", "s", "", "Disk size in GB")

	// Add subcommands
	rootCmd.AddCommand(buildCommand)
	rootCmd.AddCommand(runCommand)
	rootCmd.AddCommand(shellCommand)
	rootCmd.AddCommand(imagesCommand)
	rootCmd.AddCommand(psCommand)
	rootCmd.AddCommand(stopCommand)
	rootCmd.AddCommand(rmCommand)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}