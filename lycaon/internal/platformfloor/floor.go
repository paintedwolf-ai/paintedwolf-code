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

// DarwinArches returns an independent copy of the supported architectures.
func DarwinArches() []string { return slices.Clone(darwinArches) }

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

// LinuxELFArches returns the supported package architecture names.
func LinuxELFArches() []string { return []string{"x86_64", "aarch64"} }
