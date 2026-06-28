package ducttape

import (
	"fmt"
	"os/exec"
)

// Provisioner abstracts the VM provisioner (lima)
type Provisioner interface {
	CreateVM(name string, diskImage string, cpus string, memory string, diskSize string, username string, rootPass string, cloudInitPath string) error
	StartVM(name string) error
	StopVM(name string) error
	RemoveVM(name string) error
	SSHInfo(name string) (*VMInfo, error)
}

// VMInfo holds SSH connection info for a VM
type VMInfo struct {
	Name       string
	SSHPort    int
	SSHUser    string
	SSHKeyPath string
}

// ValidateProvisioner checks that the required binary is available.
func ValidateProvisioner(name string) error {
	switch name {
	case "macadam":
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
