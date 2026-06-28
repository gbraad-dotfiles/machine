package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	di "ducttape/pkg/ducttape"
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

		maxWait := 60
		if v := os.Getenv("DUCTTAPE_WAIT"); v != "" {
			if t, err := strconv.Atoi(v); err == nil && t > 0 {
				maxWait = t
			}
		}
		var info *di.VMInfo
		var err error
		for i := 0; i < maxWait/5; i++ {
			info, err = (&LimaProvisioner{}).SSHInfo(vmName)
			if err == nil && info != nil && info.SSHPort > 0 {
				break
			}
			time.Sleep(5 * time.Second)
		}
		if err != nil || info == nil || info.SSHPort == 0 {
			return fmt.Errorf("VM %s not found or not running (try 'ducttape ps')", args[0])
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
