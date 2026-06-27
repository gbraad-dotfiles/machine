package main

// knownAliases maps short names to download URLs for well-known base disk images.
// These are direct QCOW2 download links -- no OCI wrapping.
var knownAliases = map[string]string{
	"alpine-cloud":     "ghcr.io/ducttape-infra/cloud-images/alpine-cloud",
	"fedora-cloud":     "ghcr.io/ducttape-infra/cloud-images/fedora-cloud",
	"centos-cloud":     "ghcr.io/ducttape-infra/cloud-images/centos-cloud",
	"debian-cloud":     "ghcr.io/ducttape-infra/cloud-images/debian-cloud",
	"ubuntu-cloud":     "ghcr.io/ducttape-infra/cloud-images/ubuntu-cloud",
        "opensuse-cloud":   "ghcr.io/ducttape-infra/cloud-images/opensuse-cloud", 
	"almalinux-cloud":  "ghcr.io/ducttape-infra/cloud-images/almalinux-cloud",
	"rockylinux-cloud": "ghcr.io/ducttape-infra/cloud-images/rockylinux-cloud",
        "freebsd-cloud":    "ghcr.io/ducttape-infra/cloud-images/freebsd-cloud",
}
