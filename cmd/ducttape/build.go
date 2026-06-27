package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
		cloudInitPath, _ := cmd.Flags().GetString("cloudinit")
		debugMode, _ = cmd.Flags().GetBool("debug")
		userPass := cmd.Flags().Lookup("user-pass").Value.String()
		vmUser := imageUser
		if u, _ := cmd.Flags().GetString("user"); u != "" {
			vmUser = u
		}

		if tag == "" {
			fmt.Fprintln(cmd.OutOrStderr(), "Error: --tag is required")
			os.Exit(1)
		}
		if mfPath != "" {
			if fi, err := os.Stat(mfPath); err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "Error: %q not found\n", mfPath)
				os.Exit(1)
			} else if fi.IsDir() {
				// Directory given -- look for a Machinefile inside.
				for _, name := range []string{"Machinefile", "Dockerfile", "Containerfile"} {
					candidate := filepath.Join(mfPath, name)
					if fi2, err := os.Stat(candidate); err == nil && !fi2.IsDir() {
						mfPath = candidate
						break
					}
				}
				if fi, err := os.Stat(mfPath); err != nil || fi.IsDir() {
					fmt.Fprintf(cmd.OutOrStderr(), "Error: no Machinefile, Dockerfile, or Containerfile found in %q\n", mfPath)
					os.Exit(1)
				}
			}
		}
		// If no --base given, try reading FROM from the Machinefile
		if baseSpec == "" && mfPath != "" {
			baseSpec = readFromLine(mfPath)
		}
		if baseSpec == "" {
			fmt.Fprintln(cmd.OutOrStderr(), "Error: --base is required when no FROM is in the Machinefile")
			os.Exit(1)
		}

		// Validate provisioner binary BEFORE downloading anything
		if err := validateProvisioner(provisionerName); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "Error: %v\n", err)
			os.Exit(1)
		}

		// Check if another VM is already running
		if out, _ := exec.Command("pgrep", "-c", "-f", "qemu-system-x86").Output(); len(out) > 0 && out[0] > 48 {
			fmt.Fprintf(cmd.OutOrStderr(), "Warning: Another VM appears to be running (qemu process exists).\n")
			fmt.Fprintf(cmd.OutOrStderr(), "  Use 'ducttape ps' and 'ducttape stop <name>' to clean up.\n")
		}

		ensureDirs()

		cleanup := setupEnv(cmd)
		defer cleanup()

		basePath, err := resolveBaseImage(baseSpec)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "Error resolving base image: %v\n", err)
			os.Exit(1)
		}

		tmpName := "ducttape-build-" + randomString(6)
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

		if err := p.CreateVM(tmpName, basePath, "2", "2048", "10", vmUser, rootPass, cloudInitPath); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "%s init failed: %v\n", provisionerName, err)
			os.Exit(1)
		}

		// StartVM launches QEMU and blocks until it exits, so run it in
		// the background.
		go func() {
			p.StartVM(tmpName)
		}()

		info, err := waitForSSHInfo(tmpName, p, 60*time.Second)
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
		if _, err := os.Stat(prePath); err != nil {
			prePath = filepath.Join(filepath.Dir(mfPath), "Machinefile-pre")
		}
		if _, err := os.Stat(prePath); err == nil {
			fmt.Printf("Running pre-Machinefile (%s)...\n", prePath)
			preRunner := &mf.SSHRunner{
				BaseDir:    ".",
				SshHost:    "localhost",
				SshUser:    info.SSHUser,
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
			time.Sleep(2 * time.Second)
		} else {
			fmt.Println("  (no pre-Machinefile found)")
		}
		// --- Phase 2: main Machinefile as root -------------------------
		fmt.Printf("Executing %s as root...\n", mfPath)
		rootRunner := &mf.SSHRunner{
			BaseDir:     ".",
			SshHost:     "localhost",
			SshUser:     "root",
			SshPort:     strconv.Itoa(info.SSHPort),
			SshPassword: rootPass,
		}
		if err := mf.ParseAndRunDockerfile(mfPath, rootRunner, nil); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "machinefile execution failed: %v\n", err)
			os.Exit(1)
		}

		// --- Phase 3: post-Machinefile --------------------------------
		postPath := mfPath + "-post"
		if _, err := os.Stat(postPath); err != nil {
			postPath = filepath.Join(filepath.Dir(mfPath), "Machinefile-post")
		}
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
		} else {
			fmt.Println("  (no post-Machinefile found)")
		}
		// --- Stop VM and copy disk ------------------------------------
		fmt.Println("Stopping VM...")
		if err := p.StopVM(tmpName); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "%s stop failed: %v\n", provisionerName, err)
			os.Exit(1)
		}

		var sourceDisk string
		switch provisionerName {
		case "macadam":
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
			sourceDisk = cfg.ImagePath.Path
			if sourceDisk == "" {
				fmt.Fprintf(cmd.OutOrStderr(), "could not determine disk path from config\n")
				os.Exit(1)
			}
		default:
			// Lima: disk at ~/.lima/<name>/disk
			limaDir := filepath.Join(os.Getenv("HOME"), ".lima", tmpName)
			diskFile := filepath.Join(limaDir, "disk")
			if _, err := os.Stat(diskFile); err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "Lima disk not found at %s: %v\n", diskFile, err)
				os.Exit(1)
			}
			// Flatten overlay+backing into standalone QCOW2
			fmt.Println("  Flattening disk overlay...")
			flattened := filepath.Join(os.TempDir(), "ducttape-"+tmpName+"-flat.qcow2")
			flatten := exec.Command("qemu-img", "convert", "-O", "qcow2", diskFile, flattened)
			if out, err := flatten.CombinedOutput(); err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "failed to flatten disk: %v\n%s", err, string(out))
				os.Exit(1)
			}
			defer os.Remove(flattened)
			sourceDisk = flattened
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
// readFromLine reads the FROM line from a Machinefile, expanding ARG
// references with their defaults or from build args (--build-arg KEY=VAL).
func readFromLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	env := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		if strings.HasPrefix(line, "ARG ") {
			arg := strings.TrimSpace(strings.TrimPrefix(line, "ARG "))
			if parts := strings.SplitN(arg, "=", 2); len(parts) == 2 {
				val := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
				env[strings.TrimSpace(parts[0])] = val
			}
			continue
		}
		if strings.HasPrefix(line, "FROM ") {
			from := strings.TrimSpace(strings.TrimPrefix(line, "FROM "))
			for k, v := range env {
				from = strings.ReplaceAll(from, "${"+k+"}", v)
				from = strings.ReplaceAll(from, "$"+k, v)
			}
			return from
		}
		break
	}
	return ""
}

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
