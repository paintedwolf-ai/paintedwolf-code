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
