package platformfloor_test

import (
	"strings"
	"testing"

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
