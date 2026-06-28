package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	di "ducttape/pkg/ducttape"
	"github.com/spf13/cobra"
)

const (
	envChild = "_MACHINE_RUN_CHILD"
	envReExec = "_MACHINE_RUN_REEXEC"
)

func init() {
	if os.Getenv(envChild) != "" {
		// First stage of the fork: detach and re-exec to run blocking.
		syscall.Setsid()
		os.Unsetenv(envChild)
		cmd := exec.Command(os.Args[0], os.Args[1:]...)
		cmd.Env = append(os.Environ(), envReExec+"=1")
		cmd.Stdin = nil
		cmd.Stdout = nil
		cmd.Stderr = nil
		if lf, _ := os.Create(filepath.Join(os.TempDir(), "ducttape-run.log")); lf != nil {
			cmd.Stderr = lf
			lf.Close()
		}
		cmd.Run()
		os.Exit(0)
	}
}

var runCommand = &cobra.Command{
	Use:   "run <image>",
	Short: "Run a VM from a built image",
	Long: `Start a VM in the background from a previously built image.

  ducttape run myimage                  # run with default settings
  ducttape run myimage -n web          # name the VM
  ducttape run myimage --publish 8080:80  # forward host:8080 to guest:80
  ducttape run myimage --publish 443:443/tcp  # with protocol

Use 'ducttape ps' to list running VMs and 'ducttape stop' to stop them.`,
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) < 1 {
			fmt.Fprintln(cmd.OutOrStderr(), "Error: image argument required")
			os.Exit(1)
		}
		image := args[0]
		vmName, _ := cmd.Flags().GetString("name")
		cpus, _ := cmd.Flags().GetString("cpus")
		memory, _ := cmd.Flags().GetString("memory")
		diskSize, _ := cmd.Flags().GetString("disk-size")
		provisionerName, _ := cmd.Flags().GetString("provisioner")
		publish, _ := cmd.Flags().GetStringSlice("publish")
		vmUser := imageUser
		if u, _ := cmd.Flags().GetString("user"); u != "" {
			vmUser = u
		}

		if vmName == "" {
			vmName = image
		}
		if cpus == "" {
			cpus = "2"
		}
		if memory == "" {
			memory = "2048"
		}
		if diskSize == "" {
			diskSize = "30"
		}

		diskPath := filepath.Join(imagesDir, image+".qcow2")
		if fi, err := os.Stat(diskPath); err != nil || fi.IsDir() {
			diskPath = image
			if fi, err := os.Stat(diskPath); err != nil || fi.IsDir() {
				fmt.Fprintf(cmd.OutOrStderr(), "image %q not found\n", image)
				os.Exit(1)
			}
		}

		fullName := "ducttape-" + vmName

		// If we're the re-exec'd child, run the VM (blocking).
		if os.Getenv(envReExec) != "" {
			ensureDirs()
			rootPass, _ := cmd.Flags().GetString("root-pass")
			cleanup := setupEnv(cmd)
			defer cleanup()
			var p di.Provisioner
			switch provisionerName {
				case "lima":
				p = &LimaProvisioner{}
			default:
				os.Exit(1)
			}
			if err := p.CreateVM(fullName, diskPath, cpus, memory, diskSize, vmUser, rootPass, ""); err != nil {
				os.Exit(1)
			}
				// Insert port forwards and mounts into Lima YAML before starting
				if provisionerName == "lima" && len(publish) > 0 {
					if err := addLimaPortForwards(fullName, publish); err != nil {
						fmt.Fprintf(cmd.OutOrStderr(), "port forward setup failed: %v\n", err)
						os.Exit(1)
					}
				}
				if provisionerName == "lima" && len(mountSpecs) > 0 {
					if err := addLimaMounts(fullName, mountSpecs); err != nil {
						fmt.Fprintf(cmd.OutOrStderr(), "mount setup failed: %v\n", err)
						os.Exit(1)
					}
				}

			p.StartVM(fullName)
			return
		}

		// Parent: fork a detached child and exit.
		child := exec.Command(os.Args[0], os.Args[1:]...)
		child.Env = append(os.Environ(), envChild+"=1")
		child.Stdin = nil
		child.Stdout = nil
		child.Stderr = nil
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := child.Start(); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "fork failed: %v\n", err)
			os.Exit(1)
		}
		pid := child.Process.Pid
		child.Process.Release()

		fmt.Printf("VM %s started (pid %d)\n", fullName, pid)
		fmt.Printf("  Use 'ducttape stop %s' to stop it.\n", vmName)
	},
}

// addLimaPortForwards inserts a portForwards block into the Lima instance YAML.
// publish entries are in "hostPort:guestPort" or "hostPort:guestPort/proto" format.
func addLimaPortForwards(name string, publish []string) error {
	limaYAML := filepath.Join(os.Getenv("HOME"), ".lima", name, "lima.yaml")
	data, err := os.ReadFile(limaYAML)
	if err != nil {
		return fmt.Errorf("read lima yaml: %w", err)
	}

	var pfLines []string
	for _, entry := range publish {
		proto := "tcp"
		portSpec := entry
		if slash := strings.Index(entry, "/"); slash >= 0 {
			proto = entry[slash+1:]
			portSpec = entry[:slash]
		}
		parts := strings.SplitN(portSpec, ":", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid port spec %q (use hostPort:guestPort)", entry)
		}
		pfLines = append(pfLines, fmt.Sprintf(`  - guestPort: %s
    hostPort: %s
    proto: %s`, parts[1], parts[0], proto))
	}

	block := "\nportForwards:\n" + strings.Join(pfLines, "\n") + "\n"
	data = append(data, []byte(block)...)
	return os.WriteFile(limaYAML, data, 0o644)
}

// addLimaMounts inserts a mounts block into the Lima instance YAML.
// mount entries are in "/host/path" or "/host/path:/guest/path" format.
func addLimaMounts(name string, mountSpecs []string) error {
	limaYAML := filepath.Join(os.Getenv("HOME"), ".lima", name, "lima.yaml")
	data, err := os.ReadFile(limaYAML)
	if err != nil {
		return fmt.Errorf("read lima yaml: %w", err)
	}

	var mountLines []string
	for _, entry := range mountSpecs {
		parts := strings.SplitN(entry, ":", 2)
		hostPath := parts[0]
		guestPath := hostPath
		if len(parts) == 2 {
			guestPath = parts[1]
		}
		mountLines = append(mountLines, fmt.Sprintf(`  - location: %s
    mountPoint: %s
    writable: true`, hostPath, guestPath))
	}

	block := "\nmounts:\n" + strings.Join(mountLines, "\n") + "\n"
	data = append(data, []byte(block)...)
	return os.WriteFile(limaYAML, data, 0o644)
}
