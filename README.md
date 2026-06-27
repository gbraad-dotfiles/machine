Ducttape
========

![](https://avatars.githubusercontent.com/u/296723703?s=140&v=4)

[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/ducttape-infra/ducttape/)

Ducttape is a command-line tool designed to bridge the gap between container-like workflows and virtual machine (VM) management. It allows developers to build, run, and share VM images using a syntax and lifecycle similar to Docker or Podman.

Inspired by Dockerfiles, **Machinefiles** describe how to customize a base cloud image, installing packages, configuring services, and enabling systemd units, producing a reusable QCOW2 disk image.


## Commands

| Command | Description |
|---------|-------------|
| `ducttape build -t <tag> -f Machinefile -d <base>` | Build a VM image from a base image and a Machinefile |
| `ducttape run <tag>` | Start a VM from a built image (background forked process) |
| `ducttape shell <vm> [command...]` | SSH into a running VM or run a command |
| `ducttape images` | List cached base and built images |
| `ducttape ps` | List running VMs |
| `ducttape stop <vm>` | Stop a running VM |
| `ducttape rm <vm>` | Remove a stopped VM |
| `ducttape push <tag> [registry-ref]` | Push a built image to a registry as an OCI artifact |
| `ducttape pull <alias>` | Download a base image by alias and cache it locally |
| `ducttape gvproxy` | Run the embedded gvproxy network helper |


## Quick Start

```bash
# Build a VM image with httpd installed
ducttape build -t my-httpd -f examples/Machinefile.httpd -d fedora-cloud

# Run a VM from the built image
ducttape run my-httpd

# Verify the web server
ducttape shell my-httpd curl -s http://localhost/

# Push to a registry
ducttape push my-httpd ghcr.io/myuser/my-httpd:latest
```

See [this repo](https://github.com/ducttape-infra/examples) for a few examples. This is also extensively used in my [dotfiles](https://github.com/gbraad-dotfiles/)


## Machinefile Syntax

Machinefiles use the same `FROM`, `RUN`, `USER`, `ENV`, `ARG`, `COPY`, and `ADD` instructions as Dockerfiles, but operate on a VM instead of a container:

```dockerfile
FROM fedora-cloud

RUN dnf install -y httpd && dnf clean all && systemctl enable httpd
```

The `-d` flag overrides `FROM` when specified.

> [!NOTE]
> Other commands are ignored which therefore allows you to use Containerfiles shared between containers and virtual machines


## Requirements

- Linux with KVM support (for accelerated VMs)


## Author

| [!["Gerard Braad"](http://gravatar.com/avatar/e466994eea3c2a1672564e45aca844d0.png?s=60)](http://gbraad.nl "Gerard Braad <me@gbraad.nl>") |
|---|
| [@gbraad](https://gbraad.nl/social) |
