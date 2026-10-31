package referenceuserland

import (
	"bytes"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestManifestIsCanonicalAndRecordsEveryArchitecture(t *testing.T) {
	data, err := definitions.ReadFile("userlands.json")
	testutil.FailErr(t, "read manifest", err)
	m, err := ParseManifest(data)
	testutil.FailErr(t, "parse manifest", err)
	encoded, err := m.Encode()
	testutil.FailErr(t, "encode manifest", err)
	if !bytes.Equal(encoded, data) {
		t.Fatal("userlands.json is not in the form userlands:record writes; re-encode it")
	}
	for _, id := range IDs {
		u, err := m.Userland(id)
		testutil.FailErr(t, "userland", err)
		if got := slices.Sorted(maps.Keys(u.Trees)); !slices.Equal(got, Architectures) {
			t.Errorf("userland %s records trees for %v, want %v", id, got, Architectures)
		}
	}
}

// The BSD userland differs from GNU only in the tools chimerautils replaces: both
// start from one base and archive snapshot, and every package they share is one build.
func TestBSDSharesEveryNonBSDToolWithGNU(t *testing.T) {
	m, err := LoadManifest()
	testutil.FailErr(t, "load manifest", err)
	gnu, bsd := m.Userlands[GNU], m.Userlands[BSD]
	if gnu.Base != bsd.Base || gnu.Snapshot != bsd.Snapshot {
		t.Fatalf("GNU (%s at %s) and BSD (%s at %s) start from different archives", gnu.Base, gnu.Snapshot, bsd.Base, bsd.Snapshot)
	}
	shared := 0
	for _, arch := range Architectures {
		gnuPins, bsdPins := pinMap(gnu.BuildArgs(arch)), pinMap(bsd.BuildArgs(arch))
		for name, version := range bsdPins {
			if gnuVersion, ok := gnuPins[name]; ok {
				shared++
				if gnuVersion != version {
					t.Errorf("%s: %s is %s in GNU and %s in BSD", arch, name, gnuVersion, version)
				}
			}
		}
	}
	if shared == 0 {
		t.Fatal("GNU and BSD share no packages")
	}
}

func pinMap(args []string) map[string]string {
	out := map[string]string{}
	for _, arg := range args {
		if value, ok := strings.CutPrefix(arg, "PACKAGES="); ok {
			for _, pin := range strings.Fields(value) {
				name, version, _ := strings.Cut(pin, "=")
				out[name] = version
			}
		}
	}
	return out
}

func TestParseManifestRefusesLoosePins(t *testing.T) {
	data, err := definitions.ReadFile("userlands.json")
	testutil.FailErr(t, "read manifest", err)
	valid := string(data)
	base := "docker.io/library/busybox:1.37.0-musl@sha256:5cec3fc171c87218698e85a52af7087de727372aae264a787b8112901a5b0092"
	for name, mutate := range map[string]func(string) string{
		"unknown field":        func(s string) string { return strings.Replace(s, `"shell"`, `"shel"`, 1) },
		"base by tag":          func(s string) string { return strings.Replace(s, base, "busybox:1.37.0-musl", 1) },
		"package without pin":  func(s string) string { return strings.Replace(s, `"bash": "5.2.37-2+b10"`, `"bash": ""`, 1) },
		"relative shell":       func(s string) string { return strings.Replace(s, `"shell": "/bin/sh"`, `"shell": "sh"`, 1) },
		"missing definition":   func(s string) string { return strings.Replace(s, "busybox.Containerfile", "other.Containerfile", 1) },
		"snapshot not a stamp": func(s string) string { return strings.Replace(s, "20261027T000000Z", "latest", 1) },
		"source without hash":  func(s string) string { return strings.Replace(s, "f688acfb", "zzzzzzzz", 1) },
		"tree of unknown arch": func(s string) string { return strings.Replace(s, `"arm64": "sha256:`, `"riscv64": "sha256:`, 1) },
		"override of nothing": func(s string) string {
			return strings.Replace(s, `"ninja-build": "1.12.1-1"`, `"ninja": "1.12.1-1"`, 1)
		},
		"replaced but installed": func(s string) string {
			return strings.Replace(s, `"replaced": [`+"\n"+`        "tar",`, `"replaced": [`+"\n"+`        "bash",`, 1)
		},
		"userland missing": func(s string) string { return strings.Replace(s, `"busybox": {`, `"toybox": {`, 1) },
	} {
		mutated := mutate(valid)
		if mutated == valid {
			t.Fatalf("%s: the mutation did not apply", name)
		}
		if _, err := ParseManifest([]byte(mutated)); err == nil {
			t.Errorf("%s: ParseManifest accepted it", name)
		}
	}
}

func TestBuildArgsCarryEveryPinForTheArchitecture(t *testing.T) {
	u := &Userland{
		ID: BSD, Base: "debian@sha256:" + strings.Repeat("a", 64), Snapshot: "20260101T000000Z",
		Packages:             map[string]string{"zip": "3", "bash": "5"},
		BuildPackages:        map[string]string{"gcc": "14"},
		ArchitectureVersions: map[string]map[string]string{"amd64": {"bash": "5+b1"}},
		Replaced:             []string{"tar", "sed"},
		Sources:              map[string]Source{"libxo": {Version: "2", URL: "https://x/2.tgz", SHA256: "h"}},
	}
	want := []string{"BASE=debian@sha256:" + strings.Repeat("a", 64), "SNAPSHOT=20260101T000000Z",
		"PACKAGES=bash=5+b1 zip=3", "BUILD_PACKAGES=gcc=14", "REPLACED=tar sed",
		"LIBXO_URL=https://x/2.tgz", "LIBXO_SHA256=h"}
	if got := u.BuildArgs("amd64"); !slices.Equal(got, want) {
		t.Fatalf("BuildArgs(amd64) = %q, want %q", got, want)
	}
	if got := u.BuildArgs("arm64")[2]; got != "PACKAGES=bash=5 zip=3" {
		t.Fatalf("arm64 packages = %q, want the shared versions", got)
	}
}

func TestTagFollowsTheDefinitionPinsAndRecordedTree(t *testing.T) {
	m, err := LoadManifest()
	testutil.FailErr(t, "load manifest", err)
	u := m.Userlands[GNU]
	tag := func() string {
		t.Helper()
		got, err := u.Tag("arm64")
		testutil.FailErr(t, "tag", err)
		return got
	}
	first := tag()
	if !strings.HasPrefix(first, imageRepository+":gnu-arm64-") || tag() != first {
		t.Fatalf("tag %q is not a stable GNU arm64 tag", first)
	}
	u.Packages["bash"] += "+b1"
	pinned := tag()
	u.Trees["arm64"] = "sha256:" + strings.Repeat("0", 64)
	if pinned == first || tag() == pinned {
		t.Fatal("a pin or tree change kept the old tag")
	}
	delete(u.Trees, "amd64")
	if _, err := u.Tag("amd64"); err == nil || !strings.Contains(err.Error(), "userlands:record") {
		t.Fatalf("tag without a recorded tree = %v, want a record instruction", err)
	}
}
