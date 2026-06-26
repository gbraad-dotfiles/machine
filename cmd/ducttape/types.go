package main

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
