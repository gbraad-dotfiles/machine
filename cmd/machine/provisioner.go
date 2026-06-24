package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	provider "github.com/crc-org/macadam/pkg/machinedriver/provider"
	imagepullers "github.com/crc-org/macadam/pkg/imagepullers"
	define "github.com/containers/podman/v5/pkg/machine/define"
	shim "github.com/containers/podman/v5/pkg/machine/shim"
	vmconfigs "github.com/containers/podman/v5/pkg/machine/vmconfigs"
	machine "github.com/containers/podman/v5/pkg/machine"
	machineenv "github.com/containers/podman/v5/pkg/machine/env"
)

// MacadamProvisioner implements Provisioner using the macadam Go library.
type MacadamProvisioner struct{}

func (m *MacadamProvisioner) CreateVM(name string, diskImage string, cpus string, memory string, diskSize string, username string) error {
	p, err := provider.GetProviderOrDefault("")
	if err != nil {
		return fmt.Errorf("failed to get VM provider: %w", err)
	}
	cpuVal, _ := strconv.Atoi(cpus)
	memVal, _ := strconv.Atoi(memory)
	diskSizeVal, _ := strconv.Atoi(diskSize)

	puller := imagepullers.NewNoopImagePuller(name, p.VMType())
	puller.SetSourceURI(diskImage)

	opts := define.InitOptions{
		Name:           name,
		CPUS:           uint64(cpuVal),
		Memory:         uint64(memVal),
		DiskSize:       uint64(diskSizeVal),
		Username:       username,
		SSHIdentityPath: "",
		ImagePuller:    puller,
		Image:          diskImage,
		CloudInit:      true,
		Capabilities: &define.MachineCapabilities{
			HasReadyUnit:   false,
			ForwardSockets: false,
		},
	}
	if err := shim.Init(opts, p); err != nil {
		return fmt.Errorf("failed to initialize VM: %w", err)
	}
	return nil
}

func (m *MacadamProvisioner) StartVM(name string) error {
	p, err := provider.GetProviderOrDefault("")
	if err != nil {
		return fmt.Errorf("failed to get VM provider: %w", err)
	}
	dirs, err := machineenv.GetMachineDirs(p.VMType())
	if err != nil {
		return fmt.Errorf("failed to get machine dirs: %w", err)
	}
	mc, err := vmconfigs.LoadMachineByName(name, dirs)
	if err != nil {
		return fmt.Errorf("failed to load machine config for %s: %w", name, err)
	}
	// Suppress the podman library's noisy startup messages (rootless
	// banner, "Waiting for VM to exit...") without hiding our own.
	old := os.Stdout
	defer func() { os.Stdout = old }()
	nullDev, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	defer nullDev.Close()
	os.Stdout = nullDev

	if err := shim.Start(mc, p, dirs, machine.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}
	return nil
}

func (m *MacadamProvisioner) StopVM(name string) error {
	p, err := provider.GetProviderOrDefault("")
	if err != nil {
		return fmt.Errorf("failed to get VM provider: %w", err)
	}
	dirs, err := machineenv.GetMachineDirs(p.VMType())
	if err != nil {
		return fmt.Errorf("failed to get machine dirs: %w", err)
	}
	mc, err := vmconfigs.LoadMachineByName(name, dirs)
	if err != nil {
		return fmt.Errorf("failed to load machine config for %s: %w", name, err)
	}
	if err := shim.Stop(mc, p, dirs, false); err != nil {
		return fmt.Errorf("failed to stop VM: %w", err)
	}
	// Reap the QEMU zombie left by shim.Stop so the goroutine's
	// isProcessAlive loop can exit.
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			break
		}
	}
	return nil
}

func (m *MacadamProvisioner) RemoveVM(name string) error {
	p, err := provider.GetProviderOrDefault("")
	if err != nil {
		return fmt.Errorf("failed to get VM provider: %w", err)
	}
	dirs, err := machineenv.GetMachineDirs(p.VMType())
	if err != nil {
		return fmt.Errorf("failed to get machine dirs: %w", err)
	}
	mc, err := vmconfigs.LoadMachineByName(name, dirs)
	if err != nil {
		return fmt.Errorf("failed to load machine config for %s: %w", name, err)
	}
	if err := shim.Remove(mc, p, dirs, machine.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("failed to remove VM: %w", err)
	}
	return nil
}

func (m *MacadamProvisioner) SSHInfo(name string) (*VMInfo, error) {
	p, err := provider.GetProviderOrDefault("")
	if err != nil {
		return nil, fmt.Errorf("failed to get VM provider: %w", err)
	}
	dirs, err := machineenv.GetMachineDirs(p.VMType())
	if err != nil {
		return nil, fmt.Errorf("failed to get machine dirs: %w", err)
	}
	mc, err := vmconfigs.LoadMachineByName(name, dirs)
	if err != nil {
		return nil, fmt.Errorf("failed to load machine config: %w", err)
	}
	identity := mc.SSH.IdentityPath
	if strings.HasPrefix(identity, "~/") {
		identity = filepath.Join(os.Getenv("HOME"), identity[2:])
	}
	return &VMInfo{
		Name:       name,
		SSHPort:    mc.SSH.Port,
		SSHUser:    mc.SSH.RemoteUsername,
		SSHKeyPath: identity,
	}, nil
}
