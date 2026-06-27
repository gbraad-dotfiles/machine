package main

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

var ipCommand = &cobra.Command{
	Use:   "ip <vm>",
	Short: "Show the guest IP address of a running VM",
	Long: `Show the guest IP address of a running VM.

  ducttape ip myvm

For Lima VMs this queries the guest via 'limactl shell'.
The guest IP may not be directly reachable from the host;
use 'ducttape ports' for the forwarded addresses.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		vmName := "ducttape-" + strings.TrimPrefix(args[0], "ducttape-")

		// Try Lima via limactl shell
		if _, lookErr := exec.LookPath("limactl"); lookErr == nil {
			out, err := exec.Command("limactl", "shell", vmName, "hostname", "-I").Output()
			if err == nil {
				ips := strings.Fields(string(out))
				for _, ip := range ips {
					if strings.Contains(ip, ".") {
						fmt.Println(ip)
						return nil
					}
				}
			}
		}

		// Fallback: macadam
		info, err := readSSHInfo(vmName)
		if err != nil {
			return fmt.Errorf("VM %s not found: %w", args[0], err)
		}
		fmt.Printf("127.0.0.1:%d\n", info.SSHPort)
		return nil
	},
}
