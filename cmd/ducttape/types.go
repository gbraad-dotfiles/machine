package main

import (
	"fmt"
	"os/exec"
)

// Provisioner abstracts the VM provisioner (macadam, lima, etc.)
type Provisioner interface {
	// CreateVM creates a VM with the given parameters.
	CreateVM(name string, diskImage string, cpus string, memory string, diskSize string, username string, rootPass string, cloudInitPath string) error
	// StartVM starts the VM.
	StartVM(name string) error
	// StopVM stops the VM.
	StopVM(name string) error
	// RemoveVM removes the VM.
	RemoveVM(name string) error
	// SSHInfo returns connection info for the VM.
	SSHInfo(name string) (*VMInfo, error)
}

// VMInfo holds SSH connection info for a VM
type VMInfo struct {
	Name       string
	SSHPort    int
	SSHUser    string
	SSHKeyPath string
}

// provisionerForName returns a Provisioner for the given name.
func provisionerForName(name string) Provisioner {
	switch name {
	case "macadam":
		return &MacadamProvisioner{}
	default:
		return &LimaProvisioner{}
	}
}

// validateProvisioner checks that the required binary is available
// before any expensive operations (downloads, VM creation).
func validateProvisioner(name string) error {
	switch name {
	case "macadam":
		// macadam is used as a Go library -- no external binary needed
		return nil
	case "lima":
		if _, err := exec.LookPath("limactl"); err != nil {
			return fmt.Errorf("lima provisioner requires 'limactl' binary: not found in PATH")
		}
		return nil
	default:
		return fmt.Errorf("unknown provisioner: %s", name)
	}
}
