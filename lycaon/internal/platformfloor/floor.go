// Package platformfloor shares desktop platform requirements with build scripts.
package platformfloor

import (
	_ "embed"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

//go:embed macos_floor.txt
var macOSFloorRaw string

// MacOSMin is the deployment target / LSMinimumSystemVersion.
func MacOSMin() string { return strings.TrimSpace(macOSFloorRaw) }

// MacOSMinParts is MacOSMin split for numeric comparison.
func MacOSMinParts() (major, minor int, err error) {
	fields := strings.Split(MacOSMin(), ".")
	if len(fields) < 2 {
		return 0, 0, fmt.Errorf("macos floor %q: want major.minor", MacOSMin())
	}
	if major, err = strconv.Atoi(fields[0]); err != nil {
		return 0, 0, fmt.Errorf("macos floor major: %w", err)
	}
	if minor, err = strconv.Atoi(fields[1]); err != nil {
		return 0, 0, fmt.Errorf("macos floor minor: %w", err)
	}
	return major, minor, nil
}

// GoToolchainMacOSMinMajor is the toolchain deployment minimum.
const GoToolchainMacOSMinMajor = 12

// darwinArches lists the shipped executable slices.
var darwinArches = []string{"arm64"}

// machOArchForGoArch maps compiler names to release artifact names.
var machOArchForGoArch = map[string]string{
	"arm64": "aarch64",
}

// DarwinArches returns an independent copy of the supported architectures.
func DarwinArches() []string { return slices.Clone(darwinArches) }

// MachOArchForGoArch translates a compiler architecture to its artifact name.
func MachOArchForGoArch(goArch string) (string, bool) {
	machO, ok := machOArchForGoArch[goArch]
	return machO, ok
}

// MachOArches preserves the configured architecture order.
func MachOArches() []string {
	out := make([]string, 0, len(darwinArches))
	for _, goArch := range darwinArches {
		if machO, ok := machOArchForGoArch[goArch]; ok {
			out = append(out, machO)
		}
	}
	return out
}

//go:embed linux_floor.txt
var linuxFloorRaw string

// linuxFloor parses the embedded `key value` lines once; a malformed line is
// dropped so the accessors return "" instead of panicking.
var linuxFloor = func() map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(linuxFloorRaw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			out[fields[0]] = fields[1]
		}
	}
	return out
}()

// LinuxKernelMin is the oldest kernel release the host runs on.
func LinuxKernelMin() string { return linuxFloor["kernel"] }

// LinuxGlibcMin is the oldest C library the host links against.
func LinuxGlibcMin() string { return linuxFloor["glibc"] }

// LinuxWebKitGTKMin is the oldest WebKitGTK the desktop shell renders in.
func LinuxWebKitGTKMin() string { return linuxFloor["webkitgtk"] }

// LinuxGTK3Min is the oldest GTK 3 with Wayland activation (xdg_activation_v1).
func LinuxGTK3Min() string { return linuxFloor["gtk3"] }

// linuxArches lists the shipped Linux executables.
var linuxArches = []string{"amd64", "arm64"}

// elfArchForGoArch maps compiler names to package architecture names.
var elfArchForGoArch = map[string]string{
	"amd64": "x86_64",
	"arm64": "aarch64",
}

// LinuxArches returns an independent copy of the supported architectures.
func LinuxArches() []string { return slices.Clone(linuxArches) }

// ELFArchForGoArch translates a compiler architecture to its package name.
func ELFArchForGoArch(goArch string) (string, bool) {
	elf, ok := elfArchForGoArch[goArch]
	return elf, ok
}

// LinuxELFArches preserves the configured architecture order.
func LinuxELFArches() []string {
	out := make([]string, 0, len(linuxArches))
	for _, goArch := range linuxArches {
		if elf, ok := elfArchForGoArch[goArch]; ok {
			out = append(out, elf)
		}
	}
	return out
}
