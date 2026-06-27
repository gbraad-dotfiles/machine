package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

var shellCommand = &cobra.Command{
	Use:                "shell <vm> [command...]",
	Short:              "Open an SSH shell or run a command in a running VM",
	DisableFlagParsing: true,
	Long: `Connect to a running VM via SSH.

  ducttape shell myvm              -- opens interactive shell
  ducttape shell myvm -- command   -- runs a command and exits`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("vm name required")
		}
		vmName := "ducttape-" + strings.TrimPrefix(args[0], "ducttape-")

		// Try Lima first, then macadam
		var info *VMInfo
		var err error
		if _, lookErr := exec.LookPath("limactl"); lookErr == nil {
			info, err = (&LimaProvisioner{}).SSHInfo(vmName)
		}
		if info == nil || err != nil {
			info, err = readSSHInfo(vmName)
		}
		if err != nil {
			return fmt.Errorf("VM %s not found or not running: %w", args[0], err)
		}

		sshArgs := []string{
			"-i", info.SSHKeyPath,
			"-p", strconv.Itoa(info.SSHPort),
			"-o", "StrictHostKeyChecking=no",
			fmt.Sprintf("%s@localhost", info.SSHUser),
		}

		if len(args) > 1 {
			sshArgs = append(sshArgs, args[1:]...)
		}

		binary, err := exec.LookPath("ssh")
		if err != nil {
			return fmt.Errorf("ssh not found: %w", err)
		}
		return syscall.Exec(binary, append([]string{"ssh"}, sshArgs...), os.Environ())
	},
}
