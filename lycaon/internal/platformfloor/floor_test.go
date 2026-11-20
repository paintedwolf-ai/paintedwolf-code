package platformfloor

import (
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/dotversion"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMacOSMinIsBareVersion(t *testing.T) {
	got := MacOSMin()
	if got == "" {
		t.Fatal("MacOSMin is empty: macos_floor.txt must carry the floor")
	}
	if strings.ContainsFunc(got, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }) {
		t.Fatalf("MacOSMin %q contains whitespace: macos_floor.txt must be one bare version line", got)
	}
}

func TestMacOSMinPartsAtOrAboveGoToolchain(t *testing.T) {
	major, minor, err := MacOSMinParts()
	if err != nil {
		testutil.FailErr(t, "parse macOS floor", err)
	}
	if major < GoToolchainMacOSMinMajor {
		t.Fatalf("macOS floor major %d is below the Go toolchain floor %d: the product floor may never drop below what the toolchain itself requires",
			major, GoToolchainMacOSMinMajor)
	}
	if minor < 0 {
		t.Fatalf("macOS floor minor %d is negative", minor)
	}
}

func TestDarwinArchesIsNotAliasedToCallers(t *testing.T) {
	first := DarwinArches()
	if len(first) == 0 {
		t.Fatal("DarwinArches is empty")
	}
	first[0] = "mutated"
	if DarwinArches()[0] == "mutated" {
		t.Fatal("DarwinArches returned an aliased slice: callers can rewrite the shipped arch set")
	}
}

func TestLinuxFloorValuesAreBareDottedVersions(t *testing.T) {
	cases := map[string]string{
		"kernel":    LinuxKernelMin(),
		"glibc":     LinuxGlibcMin(),
		"webkitgtk": linuxFloor["webkitgtk"],
		"gtk3":      linuxFloor["gtk3"],
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
	cmp, err := dotversion.Compare(LinuxKernelMin(), "6.2")
	testutil.FailErr(t, "compare kernel floor", err)
	if cmp < 0 {
		t.Fatalf("kernel floor %q is below 6.2: Landlock ABI 3 is the confinement baseline", LinuxKernelMin())
	}
}

func TestLinuxGlibcFloorCoversToolchain(t *testing.T) {
	cmp, err := dotversion.Compare(LinuxGlibcMin(), "2.39")
	testutil.FailErr(t, "compare glibc floor", err)
	if cmp < 0 {
		t.Fatalf("glibc floor %q is below 2.39: the shipped binaries link against the toolchain floor", LinuxGlibcMin())
	}
}

func TestHostOSMinMatchesTheRunningPlatform(t *testing.T) {
	var want string
	switch runtime.GOOS {
	case "darwin":
		want = MacOSMin()
	case "linux":
		want = LinuxKernelMin()
	}
	if got := HostOSMin(); got != want {
		t.Fatalf("HostOSMin on %s = %q, want %q", runtime.GOOS, got, want)
	}
}

func TestLinuxELFArchesIsNotAliasedToCallers(t *testing.T) {
	first := LinuxELFArches()
	first[0] = "mutated"
	if LinuxELFArches()[0] == "mutated" {
		t.Fatal("LinuxELFArches returned an aliased slice")
	}
}
