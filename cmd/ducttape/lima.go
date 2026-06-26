package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LimaProvisioner implements Provisioner using the limactl binary.
type LimaProvisioner struct{}

func (l *LimaProvisioner) CreateVM(name string, diskImage string, cpus string, memory string, diskSize string, username string, rootPass string, cloudInitPath string) error {
	args := []string{
		"create",
		"--name", name,
		"--tty=false",
		"--cpus", cpus,
		"--memory", memory,
		"--disk-size", diskSize,
		diskImage,
	}
	return runCmd("limactl", args...)
}

func (l *LimaProvisioner) StartVM(name string) error {
	return runCmd("limactl", "start", name)
}

func (l *LimaProvisioner) StopVM(name string) error {
	return runCmd("limactl", "stop", name)
}

func (l *LimaProvisioner) RemoveVM(name string) error {
	return runCmd("limactl", "delete", "-f", name)
}

func (l *LimaProvisioner) SSHInfo(name string) (*VMInfo, error) {
	out, err := exec.Command("limactl", "list", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list lima instances: %w", err)
	}
	var instances []struct {
		Instance struct {
			Name string `json:"name"`
			SSH  struct {
				Port         int    `json:"port"`
				Host         string `json:"host"`
				User         string `json:"user"`
				IdentityPath string `json:"identityPath"`
			} `json:"ssh"`
		} `json:"instance"`
	}
	if err := json.Unmarshal(out, &instances); err != nil {
		return nil, fmt.Errorf("failed to parse lima list json: %w", err)
	}
	for _, inst := range instances {
		if inst.Instance.Name == name {
			identity := inst.Instance.SSH.IdentityPath
			if strings.HasPrefix(identity, "~/") {
				identity = filepath.Join(os.Getenv("HOME"), identity[2:])
			}
			return &VMInfo{
				Name:       name,
				SSHPort:    inst.Instance.SSH.Port,
				SSHUser:    inst.Instance.SSH.User,
				SSHKeyPath: identity,
			}, nil
		}
	}
	return nil, fmt.Errorf("lima instance %s not found", name)
}
