package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

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
		// Log child stderr to temp for debugging
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
Use 'ducttape ps' to list running VMs and 'machine stop' to stop them.`,
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
			// In re-exec child, flags are available via cmd
			rootPass, _ := cmd.Flags().GetString("root-pass")
			cleanup := setupEnv(cmd)
			defer cleanup()
			var p Provisioner
			switch provisionerName {
			case "macadam":
				p = &MacadamProvisioner{}
			case "lima":
				p = &LimaProvisioner{}
			default:
				os.Exit(1)
			}
			if err := p.CreateVM(fullName, diskPath, cpus, memory, diskSize, vmUser, rootPass, ""); err != nil {
				os.Exit(1)
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
