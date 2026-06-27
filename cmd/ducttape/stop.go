package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// stopCommand stops a VM using the default provisioner.
var stopCommand = &cobra.Command{
	Use:   "stop <vm>",
	Short: "Stop a running VM",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) < 1 {
			fmt.Fprintln(cmd.OutOrStderr(), "Error: vm argument required")
			os.Exit(1)
		}
		vm := "ducttape-" + strings.TrimPrefix(args[0], "ducttape-")
		p := provisionerForName(defaultProv)
		if err := p.StopVM(vm); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "error stopping VM %s: %v\n", vm, err)
			os.Exit(1)
		}
		fmt.Printf("Stopped VM %s\n", vm)
	},
}
