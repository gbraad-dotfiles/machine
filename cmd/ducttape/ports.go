package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var portsCommand = &cobra.Command{
	Use:   "ports <vm> [guestPort]",
	Short: "Show port forwards for a running VM",
	Long: `Show or query port forwards for a running VM.

  ducttape ports myvm              # list all forwards
  ducttape ports myvm :80          # look up host mapping for guest port 80
  curl http://$(ducttape ports test :80)/

Port forwards are set at run time with --publish:
  ducttape run myimage --publish 8080:80`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		vmName := "ducttape-" + strings.TrimPrefix(args[0], "ducttape-")

		yamlPath := filepath.Join(os.Getenv("HOME"), ".lima", vmName, "lima.yaml")
		data, err := os.ReadFile(yamlPath)
		if err != nil {
			return fmt.Errorf("VM %s not found or not a Lima VM", args[0])
		}

		lookup := ""
		if len(args) > 1 {
			lookup = strings.TrimPrefix(args[1], ":")
		}

		// Parse portForwards from YAML
		lines := strings.Split(string(data), "\n")
		inPF := false
		var forwards []string
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "portForwards:" {
				inPF = true
				continue
			}
			if inPF {
				if line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
					break
				}
				forwards = append(forwards, trimmed)
			}
		}

		if len(forwards) == 0 {
			return fmt.Errorf("no port forwards configured for %s", args[0])
		}

		// Parse entries
		var guestPort, hostPort, proto string
		found := false
		for _, f := range forwards {
			f = strings.TrimPrefix(f, "- ")
			if strings.HasPrefix(f, "guestPort:") {
				guestPort = strings.TrimSpace(strings.TrimPrefix(f, "guestPort:"))
			} else if strings.HasPrefix(f, "hostPort:") {
				hostPort = strings.TrimSpace(strings.TrimPrefix(f, "hostPort:"))
			} else if strings.HasPrefix(f, "proto:") {
				proto = strings.TrimSpace(strings.TrimPrefix(f, "proto:"))
			}

			if guestPort != "" && hostPort != "" {
				if proto == "" {
					proto = "tcp"
				}
				if lookup != "" {
					if guestPort == lookup {
						fmt.Printf("127.0.0.1:%s\n", hostPort)
						found = true
					}
				} else {
					if !found {
						fmt.Printf("Port forwards for %s:\n", args[0])
						found = true
					}
					fmt.Printf("  127.0.0.1:%s -> guest:%s (%s)\n", hostPort, guestPort, proto)
				}
				guestPort, hostPort, proto = "", "", ""
			}
		}

		if lookup != "" && !found {
			return fmt.Errorf("guest port %s not forwarded", lookup)
		}
		return nil
	},
}
