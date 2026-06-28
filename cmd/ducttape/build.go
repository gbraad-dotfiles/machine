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

var buildCommand = &cobra.Command{
	Use:   "build",
	Short: "Build a new image from a base image and a Machinefile",
	RunE: func(cmd *cobra.Command, args []string) error {
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
			return fmt.Errorf("--tag is required")
		}
		if mfPath != "" {
			if fi, err := os.Stat(mfPath); err != nil {
				return fmt.Errorf("file %q not found", mfPath)
			} else if fi.IsDir() {
				for _, name := range []string{"Machinefile", "Dockerfile", "Containerfile"} {
					candidate := filepath.Join(mfPath, name)
					if fi2, err := os.Stat(candidate); err == nil && !fi2.IsDir() {
						mfPath = candidate
						break
					}
				}
				if fi, err := os.Stat(mfPath); err != nil || fi.IsDir() {
					return fmt.Errorf("no Machinefile, Dockerfile, or Containerfile found in %q", mfPath)
				}
			}
		}
		if baseSpec == "" && mfPath != "" {
			baseSpec = readFromLine(mfPath)
		}
		if baseSpec == "" {
			return fmt.Errorf("--base is required when no FROM is in the Machinefile")
		}
		if err := validateProvisioner(provisionerName); err != nil {
			return err
		}

		ensureDirs()
		cleanup := setupEnv(cmd)
		defer cleanup()

		basePath, err := resolveBaseImage(baseSpec)
		if err != nil {
			return fmt.Errorf("resolving base image: %w", err)
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
			return fmt.Errorf("unknown provisioner: %s", provisionerName)
		}

		if err := p.CreateVM(tmpName, basePath, "2", "2048", "10", vmUser, rootPass, cloudInitPath); err != nil {
			return fmt.Errorf("%s init failed: %w", provisionerName, err)
		}

		if provisionerName == "lima" && len(mountSpecs) > 0 {
			if err := addLimaMounts(tmpName, mountSpecs); err != nil {
				return fmt.Errorf("mount setup failed: %w", err)
			}
		}

		go func() {
			p.StartVM(tmpName)
		}()

		info, err := waitForSSHInfo(tmpName, p, 180*time.Second)
		if err != nil {
			return fmt.Errorf("failed to get VM SSH info: %w", err)
		}
		fmt.Printf("Waiting for VM (port %d, user %s)...\n", info.SSHPort, info.SSHUser)
		if err := waitForSSH(info, 5*time.Minute); err != nil {
			return fmt.Errorf("timeout waiting for SSH: %w", err)
		}
		fmt.Println("VM ready.")

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
				return fmt.Errorf("pre-Machinefile failed: %w", err)
			}
			time.Sleep(2 * time.Second)
		} else {
			fmt.Println("  (no pre-Machinefile found)")
		}

		fmt.Printf("Executing %s as root...\n", mfPath)
		rootRunner := &mf.SSHRunner{
			BaseDir:     ".",
			SshHost:     "localhost",
			SshUser:     "root",
			SshPort:     strconv.Itoa(info.SSHPort),
			SshPassword: rootPass,
		}
		if err := mf.ParseAndRunDockerfile(mfPath, rootRunner, nil); err != nil {
			return fmt.Errorf("machinefile execution failed: %w", err)
		}

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
				return fmt.Errorf("post-Machinefile failed: %w", err)
			}
		} else {
			fmt.Println("  (no post-Machinefile found)")
		}

		fmt.Println("Stopping VM...")
		if err := p.StopVM(tmpName); err != nil {
			return fmt.Errorf("%s stop failed: %w", provisionerName, err)
		}

		var sourceDisk string
		switch provisionerName {
		case "macadam":
			cfgPath := filepath.Join(configDir, tmpName+".json")
			data, err := os.ReadFile(cfgPath)
			if err != nil {
				return fmt.Errorf("failed to read config after stop: %w", err)
			}
			var cfg struct {
				ImagePath struct {
					Path string `json:"Path"`
				} `json:"ImagePath"`
			}
			if err := json.Unmarshal(data, &cfg); err != nil {
				return fmt.Errorf("failed to parse config: %w", err)
			}
			sourceDisk = cfg.ImagePath.Path
			if sourceDisk == "" {
				return fmt.Errorf("could not determine disk path from config")
			}
		default:
			limaDir := filepath.Join(os.Getenv("HOME"), ".lima", tmpName)
			diskFile := filepath.Join(limaDir, "disk")
			if _, err := os.Stat(diskFile); err != nil {
				return fmt.Errorf("Lima disk not found at %s: %w", diskFile, err)
			}
			fmt.Println("  Flattening disk overlay...")
			flattened := filepath.Join(os.TempDir(), "ducttape-"+tmpName+"-flat.qcow2")
			flatten := exec.Command("qemu-img", "convert", "-O", "qcow2", diskFile, flattened)
			if out, err := flatten.CombinedOutput(); err != nil {
				return fmt.Errorf("failed to flatten disk: %v\n%s", err, string(out))
			}
			defer os.Remove(flattened)
			sourceDisk = flattened
		}

		destDisk := filepath.Join(imagesDir, tag+".qcow2")
		if err := copyFile(sourceDisk, destDisk); err != nil {
			return fmt.Errorf("failed to copy disk: %w", err)
		}
		fmt.Printf("Built image %s saved to %s\n", tag, destDisk)
		return nil
	},
}

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
	return nil, fmt.Errorf("timed out waiting for SSH info (180s) for %s", name)
}
