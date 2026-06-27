package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// rmCommand removes a VM using the default provisioner.
var rmCommand = &cobra.Command{
	Use:   "rm <vm>",
	Short: "Remove a VM",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) < 1 {
			fmt.Fprintln(cmd.OutOrStderr(), "Error: vm argument required")
			os.Exit(1)
		}
		vm := "ducttape-" + strings.TrimPrefix(args[0], "ducttape-")
		p := provisionerForName(defaultProv)
		if err := p.RemoveVM(vm); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "error removing VM %s: %v\n", vm, err)
			os.Exit(1)
		}
		fmt.Printf("Removed VM %s\n", vm)
	},
}
