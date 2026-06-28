package main

import (
	"fmt"
	"os"
	"path/filepath"

	di "ducttape/pkg/ducttape"
	"github.com/spf13/cobra"
)


var runCommand = &cobra.Command{
	Use:   "run <image>",
	SilenceUsage: true,
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
		publish, _ = cmd.Flags().GetStringSlice("publish")
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

		rootPass, _ := cmd.Flags().GetString("root-pass")
		ensureDirs()
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

		p.StartVM(fullName)
		fmt.Printf("VM %s started\n", fullName)
		fmt.Printf("  Use 'ducttape stop %s' to stop it.\n", vmName)
	},
}
