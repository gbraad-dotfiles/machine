package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// LimaProvisioner implements Provisioner using the limactl binary.
type LimaProvisioner struct{}

func (l *LimaProvisioner) CreateVM(name string, diskImage string, cpus string, memory string, diskSize string, username string, rootPass string, cloudInitPath string) error {
	provision := `provision:
- mode: system
  script: |
    #!/bin/sh
    set -e
    echo "$(date): ducttape provision start"
`

	// If custom cloud-init provided, copy its content as a provision script
	if cloudInitPath != "" {
		data, err := os.ReadFile(cloudInitPath)
		if err == nil {
			provision += fmt.Sprintf("    cat > /tmp/user-data << 'CIEOF'\n%s\nCIEOF\n", string(data))
		}
	}

	// Set root password for SSH
	if rootPass != "" {
		provision += fmt.Sprintf(`    echo 'root:%s' | chpasswd
`, rootPass)
	}

	yaml := fmt.Sprintf(`# ducttape-generated lima config
images:
- location: "%s"
arch: "%s"
cpus: %s
memory: "%sMiB"
disk: "%sGiB"
mounts: []
containerd:
  system: false
  user: false
ssh:
  localPort: 0
  loadDotSSHPubKeys: true
  forwardAgent: false
%s`, diskImage, archForQEMU(), cpus, memory, diskSize, provision)

	yamlPath := filepath.Join(os.TempDir(), "ducttape-lima-"+name+".yaml")
	if err := os.WriteFile(yamlPath, []byte(yaml), 0o644); err != nil {
		return fmt.Errorf("write lima config: %w", err)
	}

	args := []string{
		"create",
		"--name", name,
		"--tty=false",
		yamlPath,
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
	user := os.Getenv("USER")
	identity := filepath.Join(os.Getenv("HOME"), ".lima", name, "ssh")
	port := 0

	// limactl list --json can output a single object or an array.
	out, err := exec.Command("limactl", "list", "--json").Output()
	if err == nil {
		port, identity = parseLimaListJSON(out, name, identity)
	}

	if port == 0 {
		return nil, fmt.Errorf("could not determine SSH port for lima instance %s (is it running?)", name)
	}

	return &VMInfo{
		Name:       name,
		SSHPort:    port,
		SSHUser:    user,
		SSHKeyPath: identity,
	}, nil
}

// parseLimaListJSON parses "limactl list --json" output which can be either
// a single object {} or an array [{}]. Returns (port, identityFile).
func parseLimaListJSON(data []byte, name string, defaultIdentity string) (int, string) {
	type instance struct {
		Name         string `json:"name"`
		SSHLocalPort int    `json:"sshLocalPort"`
		IdentityFile string `json:"IdentityFile"`
	}

	// Single object
	var single instance
	if err := json.Unmarshal(data, &single); err == nil && single.Name == name {
		if single.IdentityFile != "" {
			return single.SSHLocalPort, single.IdentityFile
		}
		return single.SSHLocalPort, defaultIdentity
	}

	// Array
	var arr []instance
	if err := json.Unmarshal(data, &arr); err == nil {
		for _, inst := range arr {
			if inst.Name == name {
				if inst.IdentityFile != "" {
					return inst.SSHLocalPort, inst.IdentityFile
				}
				return inst.SSHLocalPort, defaultIdentity
			}
		}
	}

	return 0, defaultIdentity
}

func archForQEMU() string {
	return "x86_64"
}
