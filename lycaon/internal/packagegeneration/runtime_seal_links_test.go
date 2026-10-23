package packagegeneration

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func sealLinkFixture() *RuntimeSeal {
	seal, _ := sealFixture()
	seal.Links = []RuntimeLink{
		{Path: "/usr/lib/current.so", Target: "../../etc//alternatives/current.so", ResolvedPath: "/usr/lib/lib.so",
			CapturedPath: "runtime/lib.so", Origin: RuntimeOrigin{Kind: "native", SelectedRoot: "/usr/lib"}},
		{Path: "/etc/alternatives/current.so", Target: "/usr/lib/lib.so", ResolvedPath: "/usr/lib/lib.so",
			CapturedPath: "runtime/lib.so", Origin: RuntimeOrigin{Kind: "native", SelectedRoot: "/usr/lib"},
			Observation: &KernelObservation{Device: 8, Inode: 42, Mode: 0120777}},
	}
	return seal
}

func TestRuntimeSealLinkProvenancePreservesAlternativesWithoutAddingAuthority(t *testing.T) {
	seal := sealLinkFixture()
	original := seal.Links[0]
	raw, identity, err := MarshalRuntimeSeal(seal)
	testutil.FailErr(t, "encode validated alternatives provenance", err)
	decoded, _, _, err := ReadRuntimeSeal(bytes.NewReader(raw))
	testutil.FailErr(t, "decode alternatives provenance", err)
	if seal.Links[0] != original || decoded.Links[1].Target != original.Target || len(decoded.Aliases) != 2 {
		t.Fatal("codec changed observed target text, producer order or executable aliases")
	}
	seal.Links[0], seal.Links[1] = seal.Links[1], seal.Links[0]
	reordered, reorderedIdentity, err := MarshalRuntimeSeal(seal)
	testutil.FailErr(t, "encode reordered observations", err)
	if !bytes.Equal(raw, reordered) || identity != reorderedIdentity {
		t.Fatal("observation order changed canonical identity")
	}
	seal.Links[0].Target += "/"
	_, changedIdentity, err := MarshalRuntimeSeal(seal)
	testutil.FailErr(t, "retain different original target text", err)
	if changedIdentity == identity {
		t.Fatal("identity omitted exact link provenance")
	}
}

func TestRuntimeSealLinkProvenanceBindsDirectoryAndExactFileAliases(t *testing.T) {
	seal := sealLinkFixture()
	seal.Links = []RuntimeLink{{Path: "/etc/alternatives/library-dir", Target: "/usr/lib", ResolvedPath: "/usr/lib",
		CapturedPath: "runtime", Origin: RuntimeOrigin{Kind: "native", SelectedRoot: "/usr/lib"}}}
	_, _, err := MarshalRuntimeSeal(seal)
	testutil.FailErr(t, "bind selected retained directory", err)
	seal.Aliases = []RuntimeAlias{{Path: "/usr/lib/lib.so", Target: "runtime/lib.so", Kind: "file"}}
	seal.Links = sealLinkFixture().Links
	_, _, err = MarshalRuntimeSeal(seal)
	testutil.FailErr(t, "bind exact physical file projection without guessed layout", err)
}

func TestRuntimeSealRefusesConflictingAndUnselectedLinkProvenance(t *testing.T) {
	fixture := sealLinkFixture()
	cases := map[string]func(*RuntimeSeal){
		"duplicate same observation": func(s *RuntimeSeal) { s.Links = append(s.Links, s.Links[0]) },
		"conflicting observation": func(s *RuntimeSeal) {
			other := s.Links[0]
			other.Target = "different"
			s.Links = append(s.Links, other)
		},
		"outside selected root":      func(s *RuntimeSeal) { s.Links[0].ResolvedPath = "/tmp/unselected.so" },
		"prefix sibling root":        func(s *RuntimeSeal) { s.Links[0].ResolvedPath = "/usr/library/lib.so" },
		"unselected provenance kind": func(s *RuntimeSeal) { s.Links[0].Origin.Kind = "observed-root" },
		"relative observed path":     func(s *RuntimeSeal) { s.Links[0].Path = "current.so" },
		"noncanonical resolved path": func(s *RuntimeSeal) { s.Links[0].ResolvedPath = "/usr/lib/../lib/lib.so" },
		"different retained object":  func(s *RuntimeSeal) { s.Links[0].CapturedPath = "inputs/helper" },
		"retained symlink":           func(s *RuntimeSeal) { s.Links[0].CapturedPath = "runtime/lib-alias.so" },
		"missing retained object":    func(s *RuntimeSeal) { s.Links[0].CapturedPath = "runtime/missing" },
		"no selected projection": func(s *RuntimeSeal) {
			s.Aliases = []RuntimeAlias{{Path: "/other/lib", Target: "runtime", Kind: "directory"}}
		},
		"empty original target":        func(s *RuntimeSeal) { s.Links[0].Target = "" },
		"oversized original target":    func(s *RuntimeSeal) { s.Links[0].Target = strings.Repeat("x", 4096) },
		"NUL original target":          func(s *RuntimeSeal) { s.Links[0].Target = "lib\x00.so" },
		"control original target":      func(s *RuntimeSeal) { s.Links[0].Target = "lib\n.so" },
		"invalid UTF8 original target": func(s *RuntimeSeal) { s.Links[0].Target = string([]byte{0xff}) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			seal := cloneSeal(t, fixture)
			mutate(seal)
			if _, _, err := MarshalRuntimeSeal(seal); err == nil {
				t.Fatal("conflicting or unselected link provenance accepted")
			}
		})
	}
}

func TestRuntimeSealLinksParticipateInCanonicalShapeAndEntryBound(t *testing.T) {
	seal := sealLinkFixture()
	raw, _, err := MarshalRuntimeSeal(seal)
	testutil.FailErr(t, "encode links", err)
	var document map[string]json.RawMessage
	testutil.FailErr(t, "read fixture shape", json.Unmarshal(raw, &document))
	var links []json.RawMessage
	testutil.FailErr(t, "read link entries", json.Unmarshal(document["links"], &links))
	links[0], links[1] = links[1], links[0]
	reversed, err := json.Marshal(links)
	testutil.FailErr(t, "reverse serialized links", err)
	unsorted := bytes.Replace(raw, document["links"], reversed, 1)
	if _, _, _, err := ReadRuntimeSeal(bytes.NewReader(unsorted)); err == nil {
		t.Fatal("alternate link order accepted as canonical")
	}
	seal.Links = make([]RuntimeLink, RuntimeSealMaxEntries)
	if _, _, err := MarshalRuntimeSeal(seal); err == nil {
		t.Fatal("link provenance omitted from combined bound")
	}
	seal, _ = sealFixture()
	empty, identity, err := MarshalRuntimeSeal(seal)
	testutil.FailErr(t, "encode nil links", err)
	seal.Links = []RuntimeLink{}
	presentEmpty, presentIdentity, err := MarshalRuntimeSeal(seal)
	testutil.FailErr(t, "encode explicit empty links", err)
	if !bytes.Equal(empty, presentEmpty) || identity != presentIdentity {
		t.Fatal("empty links have multiple canonical identities")
	}
}

func TestRuntimeSealNixLinkProvenanceUsesSelectedOutputAndRetainedMapping(t *testing.T) {
	seal := sealLinkFixture()
	seal.Aliases = []RuntimeAlias{{Path: "/nix/store/selected-output", Target: "runtime", Kind: "directory"}}
	seal.Links = []RuntimeLink{{Path: "/etc/alternatives/current.so", Target: "/nix/store/selected-output/lib.so",
		ResolvedPath: "/nix/store/selected-output/lib.so", CapturedPath: "runtime/lib.so",
		Origin: RuntimeOrigin{Kind: "nix", StoreRoot: "/nix/store", SelectedRoot: "/nix/store/selected-output",
			OutputPath: "/nix/store/selected-output"}}}
	_, _, err := MarshalRuntimeSeal(seal)
	testutil.FailErr(t, "bind selected output without inode authority", err)
	seal.Links[0].Observation = &KernelObservation{UID: 65534, Device: 99, Inode: 7}
	_, _, err = MarshalRuntimeSeal(seal)
	testutil.FailErr(t, "retain diagnostic projected ownership", err)
	seal.Links[0].Origin.OutputPath = "/nix/store/other-output"
	if _, _, err := MarshalRuntimeSeal(seal); err == nil {
		t.Fatal("unselected output accepted through link provenance")
	}
}
