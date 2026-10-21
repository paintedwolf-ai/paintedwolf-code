//go:build linux

package launchguard

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func entryFixture() EntrySpec {
	return EntrySpec{
		Version: 1, ParentPID: 10, UID: 1000, GID: 1000,
		Generation:  GenerationPolicy{Version: 1, GenerationRoot: "/opt/painted-wolf-code", LogicalInstallRoot: "/opt/painted-wolf-code", ManifestSHA256: "0000000000000000000000000000000000000000000000000000000000000000"},
		Image:       pkgmanifest.Executable{Path: "/opt/painted-wolf-code/bin/painted-wolf-code", Size: 1, SHA256: "0000000000000000000000000000000000000000000000000000000000000000"},
		Environment: []string{"PATH=/usr/bin:/bin"},
	}
}

func TestEntryFrameRejectsIdentityAndTransportAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*EntrySpec)
		valid  bool
	}{
		{"selected desktop", func(*EntrySpec) {}, true},
		{"root identity", func(s *EntrySpec) { s.UID = 0 }, false},
		{"missing parent", func(s *EntrySpec) { s.ParentPID = 1 }, false},
		{"other role", func(s *EntrySpec) { s.Image.Path = "/usr/bin/sh" }, false},
		{"wrong generation", func(s *EntrySpec) { s.Generation.GenerationRoot = "/opt/other" }, false},
		{"invalid supplementary identity", func(s *EntrySpec) { s.Groups = []uint32{^uint32(0)} }, false},
		{"relative image", func(s *EntrySpec) { s.Image.Path = "painted-wolf-code" }, false},
		{"duplicate environment", func(s *EntrySpec) { s.Environment = []string{"A=one", "A=two"} }, false},
		{"environment terminator", func(s *EntrySpec) { s.Environment = []string{"A=one\x00B=two"} }, false},
		{"argument terminator", func(s *EntrySpec) { s.Arguments = []string{"one\x00two"} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := entryFixture()
			tc.mutate(&spec)
			raw, err := json.Marshal(spec)
			testutil.FailErr(t, "encode entry frame", err)
			_, err = decodeEntry(raw)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
	raw, err := json.Marshal(entryFixture())
	testutil.FailErr(t, "encode valid entry", err)
	if _, err := decodeEntry(append(raw, []byte(" {}")...)); err == nil {
		t.Fatal("accepted a second entry frame")
	}
}

func TestInitialNamespaceCannotAttestProtectedEntry(t *testing.T) {
	origin, err := os.Open("/proc/self/ns/user")
	testutil.FailErr(t, "open original namespace", err)
	defer func() { _ = origin.Close() }()
	if err := verifyNamespace(origin); err == nil {
		t.Fatal("accepted the unchanged user namespace as a protected transition")
	}
	if err := verifyNamespace(nil); err == nil {
		t.Fatal("accepted a missing original namespace descriptor")
	}
}
