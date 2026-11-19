//go:build !linux

package confine

import "github.com/lycaon/lycaon/internal/confine/linuxsandbox"

// LinuxCapabilities reports the Linux confinement facts; ok is false on every
// other platform.
func LinuxCapabilities() (linuxsandbox.Capabilities, bool) {
	return linuxsandbox.Capabilities{}, false
}

// PrivateTempFor is empty on platforms without private temp mounts.
func PrivateTempFor(string) PrivateTemp { return PrivateTemp{} }
