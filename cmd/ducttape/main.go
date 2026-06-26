package main

import (
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	userHomeDir   = os.Getenv("HOME")
		imageUser     = os.Getenv("USER")
	ducttapeHome   = userHomeDir + "/.local/share/ducttape"
	baseImagesDir = ducttapeHome + "/base-images"
	imagesDir     = ducttapeHome + "/images"
	configDir     = userHomeDir + "/.config/containers/podman/machine/qemu"
)

// init initializes Viper configuration and random seed.
func init() {
	v := viper.New()
	v.SetConfigName("ducttape")
	v.AddConfigPath("$HOME/.config")
	v.AddConfigPath(".")
	if err := v.ReadInConfig(); err == nil {
		if home := v.GetString("machine_home"); home != "" {
			ducttapeHome = home
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
	rand.Seed(time.Now().UnixNano())
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "ducttape",
		Short: "Machine CLI for managing cloud-init based VM images",
		Long:  `Ducttape provides a Docker-like workflow for building and running VM images using Machinefiles.`,
	}

	buildCommand.Flags().StringP("tag", "t", "", "Tag name for the resulting image")
	buildCommand.Flags().StringP("file", "f", "", "Path to the Machinefile")
	buildCommand.Flags().StringP("base", "d", "", "Base image (path or tag)")
	buildCommand.Flags().StringP("provisioner", "p", "macadam", "Provisioner to use (macadam|lima)")
	buildCommand.Flags().StringP("user", "u", "", "Cloud-init username (default: $USER)")
	buildCommand.Flags().String("cloudinit", "", "Path to custom cloud-init user-data file")
	buildCommand.Flags().StringP("root-pass", "", defaultRootPass, "Root password for SSH (default: "+defaultRootPass+")")
	buildCommand.Flags().StringP("user-pass", "", defaultUserPass, "User password for SSH (default: "+defaultUserPass+")")

	runCommand.Flags().StringP("name", "n", "", "Name for the VM (optional)")
	runCommand.Flags().StringP("root-pass", "", defaultRootPass, "Root password for SSH (default: "+defaultRootPass+")")
	runCommand.Flags().StringP("user", "u", "", "Cloud-init username (default: $USER)")
	runCommand.Flags().StringP("cpus", "c", "", "Number of CPUs")
	runCommand.Flags().StringP("memory", "m", "", "Memory in MB")
	runCommand.Flags().StringP("disk-size", "s", "", "Disk size in GB")
	runCommand.Flags().StringP("provisioner", "p", "macadam", "Provisioner to use (macadam|lima)")

	rootCmd.AddCommand(buildCommand)
	rootCmd.AddCommand(runCommand)
	rootCmd.AddCommand(shellCommand)
	rootCmd.AddCommand(imagesCommand)
	rootCmd.AddCommand(psCommand)
	rootCmd.AddCommand(stopCommand)
	rootCmd.AddCommand(rmCommand)
	rootCmd.AddCommand(gvproxyCommand)
	rootCmd.AddCommand(pushCommand)
	rootCmd.AddCommand(pullCommand)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
