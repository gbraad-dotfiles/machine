package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

var psCommand = &cobra.Command{
	Use:   "ps",
	Short: "List running VMs",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("VMs:")
		seen := map[string]bool{}

		if limactlPath, err := exec.LookPath("limactl"); err == nil {
			out, err := exec.Command(limactlPath, "list", "--json").Output()
			if err == nil {
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
					fmt.Printf("  %s\t[%s]\n", friendlyName, status)
					seen[friendlyName] = true
				}
			}
		}

		if len(seen) == 0 {
			fmt.Printf("  (no VMs found)\n")
		}
	},
}

type limaInstance struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func parseLimaInstanceList(data []byte) []limaInstance {
	var arr []limaInstance
	if err := json.Unmarshal(data, &arr); err == nil {
		return arr
	}
	var single limaInstance
	if err := json.Unmarshal(data, &single); err == nil && single.Name != "" {
		return []limaInstance{single}
	}
	var result []limaInstance
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var inst limaInstance
		if err := json.Unmarshal([]byte(line), &inst); err == nil && inst.Name != "" {
			result = append(result, inst)
		}
	}
	return result
}
