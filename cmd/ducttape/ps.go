package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

// psCommand lists VMs managed by ducttape (names with ducttape- prefix).
var psCommand = &cobra.Command{
	Use:   "ps",
	Short: "List running VMs",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("VMs:")
		seen := map[string]bool{}

		// Check Lima instances
		if limactlPath, err := exec.LookPath("limactl"); err == nil {
			out, err := exec.Command(limactlPath, "list", "--json").Output()
			if err == nil {
				var instances []struct {
					Name   string `json:"name"`
					Status string `json:"status"`
				}
				if err := json.Unmarshal(out, &instances); err == nil {
					for _, inst := range instances {
						if !strings.HasPrefix(inst.Name, "ducttape-") {
							continue
						}
						friendlyName := strings.TrimPrefix(inst.Name, "ducttape-")
						status := "Running"
						if inst.Status == "Stopped" {
							status = "Stopped"
						}
						fmt.Printf("  %s\t[%s]\t(lima)\n", friendlyName, status)
						seen[friendlyName] = true
					}
				}
			}
		}

		// Check macadam config dir for any VMs not shown by Lima
		entries, err := os.ReadDir(configDir)
		if err != nil {
			if !seen["_"] {
				fmt.Printf("  (no VMs found)\n")
			}
			return
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			machineName := strings.TrimSuffix(e.Name(), ".json")
			if !strings.HasPrefix(machineName, "ducttape-") {
				continue
			}
			friendlyName := strings.TrimPrefix(machineName, "ducttape-")
			if seen[friendlyName] {
				continue
			}
			status := "Stopped"
			if vmIsRunning(machineName) {
				status = "Running"
			}
			fmt.Printf("  %s\t[%s]\t(macadam)\n", friendlyName, status)
			seen[friendlyName] = true
		}

		if len(seen) == 0 {
			fmt.Printf("  (no VMs found)\n")
		}
	},
}
