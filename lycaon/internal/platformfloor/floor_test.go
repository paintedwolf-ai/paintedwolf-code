package platformfloor_test

import (
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/dotversion"
	"github.com/lycaon/lycaon/internal/platformfloor"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMacOSMinIsBareVersion(t *testing.T) {
	got := platformfloor.MacOSMin()
	if got == "" {
		t.Fatal("MacOSMin is empty: macos_floor.txt must carry the floor")
	}
	if strings.ContainsFunc(got, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }) {
		t.Fatalf("MacOSMin %q contains whitespace: macos_floor.txt must be one bare version line", got)
	}
}

func TestMacOSMinPartsAtOrAboveGoToolchain(t *testing.T) {
	major, minor, err := platformfloor.MacOSMinParts()
	if err != nil {
		testutil.FailErr(t, "parse macOS floor", err)
	}
	if major < platformfloor.GoToolchainMacOSMinMajor {
		t.Fatalf("macOS floor major %d is below the Go toolchain floor %d: the product floor may never drop below what the toolchain itself requires",
			major, platformfloor.GoToolchainMacOSMinMajor)
	}
	if minor < 0 {
		t.Fatalf("macOS floor minor %d is negative", minor)
	}
}

func TestDarwinArchesAllMapToMachONames(t *testing.T) {
	arches := platformfloor.DarwinArches()
	if len(arches) == 0 {
		t.Fatal("DarwinArches is empty: the release path ships at least one slice")
	}
	for _, goArch := range arches {
		machO, ok := platformfloor.MachOArchForGoArch(goArch)
		if !ok {
			t.Fatalf("DarwinArches entry %q has no MachOArchForGoArch mapping: artifact filenames and the updater platform key need the Mach-O spelling", goArch)
		}
		if machO == "" {
			t.Fatalf("MachOArchForGoArch(%q) is empty", goArch)
		}
	}
}

func TestMachOArchNamesAreDistinct(t *testing.T) {
	machOArches := platformfloor.MachOArches()
	if len(machOArches) != len(platformfloor.DarwinArches()) {
		t.Fatalf("MachOArches has %d entries but DarwinArches has %d: every shipped slice needs a Mach-O spelling",
			len(machOArches), len(platformfloor.DarwinArches()))
	}
	seen := make(map[string]struct{}, len(machOArches))
	for _, machO := range machOArches {
		if _, dup := seen[machO]; dup {
			t.Fatalf("Mach-O arch %q appears twice: artifact filenames would collide", machO)
		}
		seen[machO] = struct{}{}
	}
}

func TestDarwinArchesIsNotAliasedToCallers(t *testing.T) {
	first := platformfloor.DarwinArches()
	if len(first) == 0 {
		t.Fatal("DarwinArches is empty")
	}
	first[0] = "mutated"
	if platformfloor.DarwinArches()[0] == "mutated" {
		t.Fatal("DarwinArches returned an aliased slice: callers can rewrite the shipped arch set")
	}
}

func TestLinuxFloorValuesAreBareDottedVersions(t *testing.T) {
	cases := map[string]string{
		"kernel":    platformfloor.LinuxKernelMin(),
		"glibc":     platformfloor.LinuxGlibcMin(),
		"webkitgtk": platformfloor.LinuxWebKitGTKMin(),
		"gtk3":      platformfloor.LinuxGTK3Min(),
	}
	for key, got := range cases {
		if got == "" {
			t.Fatalf("Linux %s floor is empty: linux_floor.txt must carry a `%s <version>` line", key, key)
		}
		if cmp, err := dotversion.Compare(got, got); err != nil || cmp != 0 {
			t.Fatalf("Linux %s floor %q is not a bare dotted numeric version: %v", key, got, err)
		}
	}
}

func TestLinuxKernelFloorCoversLandlockABI3(t *testing.T) {
	cmp, err := dotversion.Compare(platformfloor.LinuxKernelMin(), "6.2")
	testutil.FailErr(t, "compare kernel floor", err)
	if cmp < 0 {
		t.Fatalf("kernel floor %q is below 6.2: Landlock ABI 3 is the confinement baseline", platformfloor.LinuxKernelMin())
	}
}

func TestLinuxGlibcFloorCoversToolchain(t *testing.T) {
	cmp, err := dotversion.Compare(platformfloor.LinuxGlibcMin(), "2.39")
	testutil.FailErr(t, "compare glibc floor", err)
	if cmp < 0 {
		t.Fatalf("glibc floor %q is below 2.39: the shipped binaries link against the toolchain floor", platformfloor.LinuxGlibcMin())
	}
}

func TestHostOSMinMatchesTheRunningPlatform(t *testing.T) {
	var want string
	switch runtime.GOOS {
	case "darwin":
		want = platformfloor.MacOSMin()
	case "linux":
		want = platformfloor.LinuxKernelMin()
	}
	if got := platformfloor.HostOSMin(); got != want {
		t.Fatalf("HostOSMin on %s = %q, want %q", runtime.GOOS, got, want)
	}
}

func TestLinuxArchesMapToDistinctELFNames(t *testing.T) {
	arches := platformfloor.LinuxArches()
	if len(arches) == 0 {
		t.Fatal("LinuxArches is empty: the package path ships at least one executable")
	}
	elfArches := platformfloor.LinuxELFArches()
	if len(elfArches) != len(arches) {
		t.Fatalf("LinuxELFArches has %d entries but LinuxArches has %d: every shipped executable needs a package architecture", len(elfArches), len(arches))
	}
	seen := make(map[string]struct{}, len(arches))
	for i, goArch := range arches {
		elf, ok := platformfloor.ELFArchForGoArch(goArch)
		if !ok || elf == "" {
			t.Fatalf("LinuxArches entry %q has no ELFArchForGoArch mapping", goArch)
		}
		if elf != elfArches[i] {
			t.Fatalf("LinuxELFArches[%d] = %q, want %q: order must follow LinuxArches", i, elfArches[i], elf)
		}
		if _, dup := seen[elf]; dup {
			t.Fatalf("ELF arch %q appears twice: package filenames would collide", elf)
		}
		seen[elf] = struct{}{}
	}
	if _, ok := platformfloor.ELFArchForGoArch("386"); ok {
		t.Fatal("ELFArchForGoArch mapped an unshipped architecture")
	}
}

func TestLinuxArchSlicesAreNotAliasedToCallers(t *testing.T) {
	first := platformfloor.LinuxArches()
	first[0] = "mutated"
	if platformfloor.LinuxArches()[0] == "mutated" {
		t.Fatal("LinuxArches returned an aliased slice: callers can rewrite the shipped arch set")
	}
	elf := platformfloor.LinuxELFArches()
	elf[0] = "mutated"
	if platformfloor.LinuxELFArches()[0] == "mutated" {
		t.Fatal("LinuxELFArches returned an aliased slice")
	}
}
