package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	mf "github.com/machinefile/machinefile/pkg/machinefile"
	"github.com/spf13/cobra"
)

const (
	defaultRootPass = "password"
	defaultUserPass = "password"
)

// buildCommand represents the build subcommand
var buildCommand = &cobra.Command{
	Use:   "build",
	Short: "Build a new image from a base image and a Machinefile",
	Run: func(cmd *cobra.Command, args []string) {
		tag, _ := cmd.Flags().GetString("tag")
		mfPath, _ := cmd.Flags().GetString("file")
		baseSpec, _ := cmd.Flags().GetString("base")
		provisionerName, _ := cmd.Flags().GetString("provisioner")
		rootPass := cmd.Flags().Lookup("root-pass").Value.String()
		userPass := cmd.Flags().Lookup("user-pass").Value.String()

		if tag == "" || baseSpec == "" {
			fmt.Fprintln(cmd.OutOrStderr(), "Error: --tag and --base are required")
			os.Exit(1)
		}
		ensureDirs()
	// Continuous zombie reaper: prevents QEMU/gvproxy zombies from
	// hanging the goroutine's isProcessAlive loop.
	go func() {
		for {
			for {
				var ws syscall.WaitStatus
				pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
				if pid <= 0 || err != nil {
					break
				}
			}
			time.Sleep(2 * time.Second)
		}
	}()

	cleanup := setupEnv(cmd)
		defer cleanup()

		basePath, err := resolveBaseImage(baseSpec)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "Error resolving base image: %v\n", err)
			os.Exit(1)
		}

		tmpName := "machine-build-" + randomString(6)
		var p Provisioner
		vmCleanup := func() {
			if p != nil {
				p.RemoveVM(tmpName)
			}
		}
		defer vmCleanup()

		switch provisionerName {
		case "macadam":
			p = &MacadamProvisioner{}
		case "lima":
			p = &LimaProvisioner{}
		default:
			fmt.Fprintf(cmd.OutOrStderr(), "unknown provisioner: %s\n", provisionerName)
			os.Exit(1)
		}

		if err := p.CreateVM(tmpName, basePath, "2", "2048", "10", "fedora"); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "%s init failed: %v\n", provisionerName, err)
			os.Exit(1)
		}

		// StartVM launches QEMU and blocks until it exits, so run it in
		// the background.
		go func() {
			p.StartVM(tmpName)
		}()

		// Wait for the VM config to appear, then proceed with SSH.
		info, err := waitForSSHInfo(tmpName, p, 30*time.Second)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "failed to get VM SSH info: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Waiting for VM (port %d, user %s)...\n", info.SSHPort, info.SSHUser)
		if err := waitForSSH(info, 5*time.Minute); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "timeout waiting for SSH: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("VM ready.")

		// --- Phase 1: pre-Machinefile ---------------------------------
		prePath := mfPath + "-pre"
		if _, err := os.Stat(prePath); err == nil {
			fmt.Printf("Running pre-Machinefile (%s)...\n", prePath)
			preRunner := &mf.SSHRunner{
				BaseDir:    ".",
				SshHost:    "localhost",
				SshUser:    info.SSHUser, // provisioned user (fedora)
				SshPort:    strconv.Itoa(info.SSHPort),
				SshKeyPath: info.SSHKeyPath,
			}
			preArgs := map[string]string{
				"ROOT_PASSWD": rootPass,
				"USER":        info.SSHUser,
			}
			if err := mf.ParseAndRunDockerfile(prePath, preRunner, preArgs); err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "pre-Machinefile failed: %v\n", err)
				os.Exit(1)
			}
			// sshd was restarted by pre; give it a moment
			time.Sleep(2 * time.Second)
		}

		// --- Phase 2: main Machinefile as root -------------------------
		fmt.Printf("Executing %s as root...\n", mfPath)
		rootRunner := &mf.SSHRunner{
			BaseDir:    ".",
			SshHost:    "localhost",
			SshUser:    "root",
			SshPort:    strconv.Itoa(info.SSHPort),
			SshPassword: rootPass,
		}
		if err := mf.ParseAndRunDockerfile(mfPath, rootRunner, nil); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "machinefile execution failed: %v\n", err)
			os.Exit(1)
		}

		// --- Phase 3: post-Machinefile ---------------------------------
		postPath := mfPath + "-post"
		if _, err := os.Stat(postPath); err == nil {
			fmt.Printf("Running post-Machinefile (%s)...\n", postPath)
			postRunner := &mf.SSHRunner{
				BaseDir:    ".",
				SshHost:    "localhost",
				SshUser:    info.SSHUser,
				SshPort:    strconv.Itoa(info.SSHPort),
				SshKeyPath: info.SSHKeyPath,
			}
			postArgs := map[string]string{"USER_PASSWD": userPass}
			if err := mf.ParseAndRunDockerfile(postPath, postRunner, postArgs); err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "post-Machinefile failed: %v\n", err)
				os.Exit(1)
			}
		}

		// --- Stop VM and copy disk ------------------------------------
		fmt.Println("Stopping VM...")
		// Kill QEMU by PID and wait (reap) so it cannot become a zombie.
		// Macadam/shim.Stop can hang if QEMU is a zombie.
		if pid := readQEMUPid(tmpName); pid > 0 {
			syscall.Kill(pid, syscall.SIGTERM)
			syscall.Wait4(pid, nil, 0, nil)
		}
		if err := p.StopVM(tmpName); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "%s stop failed: %v\n", provisionerName, err)
			os.Exit(1)
		}
		if err := p.StopVM(tmpName); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "%s stop failed: %v\n", provisionerName, err)
			os.Exit(1)
		}

		cfgPath := filepath.Join(configDir, tmpName+".json")
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "failed to read config after stop: %v\n", err)
			os.Exit(1)
		}
		var cfg struct {
			ImagePath struct {
				Path string `json:"Path"`
			} `json:"ImagePath"`
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "failed to parse config: %v\n", err)
			os.Exit(1)
		}
		sourceDisk := cfg.ImagePath.Path
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

// waitForSSHInfo polls SSHInfo until the config is available or timeout.
func waitForSSHInfo(name string, p Provisioner, timeout time.Duration) (*VMInfo, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := p.SSHInfo(name)
		if err == nil && info.SSHPort > 0 {
			return info, nil
		}
		time.Sleep(2 * time.Second)
	}
	return nil, fmt.Errorf("timed out waiting for SSH info for %s", name)
}
