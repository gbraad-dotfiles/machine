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
				// limactl list --json can be a single object {} or an array [{}]
				instances := parseLimaInstanceList(out)
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

		// Check macadam config dir for any VMs not shown by Lima
		entries, err := os.ReadDir(configDir)
		if err != nil {
			if len(seen) == 0 {
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

// parseLimaInstanceList parses "limactl list --json" output which can be
// either a single object {} or an array [{}].
type limaInstance struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func parseLimaInstanceList(data []byte) []limaInstance {
	// Try as array first
	var arr []limaInstance
	if err := json.Unmarshal(data, &arr); err == nil {
		return arr
	}
	// Try as single object
	var single limaInstance
	if err := json.Unmarshal(data, &single); err == nil && single.Name != "" {
		return []limaInstance{single}
	}
	return nil
}
