package packagegeneration

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRuntimeSealRefusesConflictingCaptureAndProjection(t *testing.T) {
	fixture, _ := sealFixture()
	cases := map[string]func(*RuntimeSeal){
		"source digest":          func(s *RuntimeSeal) { s.Sources[0].SHA256 = runtimeDigest([]byte("other")) },
		"source size":            func(s *RuntimeSeal) { s.Sources[0].Size++ },
		"source prefix escape":   func(s *RuntimeSeal) { s.Sources[0].Path = "/usr/library/lib.so" },
		"source duplicate":       func(s *RuntimeSeal) { s.Sources = append(s.Sources, s.Sources[0]) },
		"source captures link":   func(s *RuntimeSeal) { s.Sources[0].CapturedPath = "runtime/lib-alias.so" },
		"native Nix fields":      func(s *RuntimeSeal) { s.Sources[0].Origin.StoreRoot = "/nix/store" },
		"inventory digest":       func(s *RuntimeSeal) { s.InventorySHA256 = runtimeDigest([]byte("other")) },
		"inventory missing":      func(s *RuntimeSeal) { s.InventoryPath = "missing" },
		"anchor digest":          func(s *RuntimeSeal) { s.Anchors[0].SHA256 = runtimeDigest([]byte("other")) },
		"anchor duplicate role":  func(s *RuntimeSeal) { s.Anchors = append(s.Anchors, s.Anchors[0]) },
		"anchor invalid role":    func(s *RuntimeSeal) { s.Anchors[0].Role = "../auditor" },
		"anchor is directory":    func(s *RuntimeSeal) { s.Anchors[0].Path = "runtime" },
		"alias duplicate":        func(s *RuntimeSeal) { s.Aliases = append(s.Aliases, s.Aliases[0]) },
		"alias kind":             func(s *RuntimeSeal) { s.Aliases[0].Kind = "file" },
		"alias is link":          func(s *RuntimeSeal) { s.Aliases[1].Target = "runtime/lib-alias.so" },
		"alias root":             func(s *RuntimeSeal) { s.Aliases[0].Path = "/" },
		"alias subtree override": func(s *RuntimeSeal) { s.Aliases[1].Target = "inputs/helper" },
		"alias beneath file": func(s *RuntimeSeal) {
			s.Aliases = append(s.Aliases, RuntimeAlias{Path: "/usr/lib/helper/child", Target: "inputs/helper", Kind: "file"})
		},
		"final duplicate":      func(s *RuntimeSeal) { s.Files = append(s.Files, s.Files[0]) },
		"final escape":         func(s *RuntimeSeal) { s.Files = append(s.Files, sealFile("../outside", "x")) },
		"final missing parent": func(s *RuntimeSeal) { s.Files = append(s.Files, sealFile("missing/file", "x")) },
		"final beneath file":   func(s *RuntimeSeal) { s.Files = append(s.Files, sealFile("runtime/lib.so/child", "x")) },
		"final beneath link":   func(s *RuntimeSeal) { s.Files = append(s.Files, sealFile("runtime/lib-alias.so/child", "x")) },
		"final writable":       func(s *RuntimeSeal) { s.Files[1].Mode = 0666 },
		"file has link target": func(s *RuntimeSeal) { target := "lib.so"; s.Files[1].Target = &target },
		"link escapes":         func(s *RuntimeSeal) { target := "../../outside"; s.Files[3].Target = &target },
		"link dangling":        func(s *RuntimeSeal) { target := "missing"; s.Files[3].Target = &target },
		"link self cycle":      func(s *RuntimeSeal) { target := "lib-alias.so"; s.Files[3].Target = &target },
		"link parent is file":  func(s *RuntimeSeal) { target := "lib.so/child"; s.Files[3].Target = &target },
		"link absolute":        func(s *RuntimeSeal) { target := "/usr/lib/lib.so"; s.Files[3].Target = &target },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			seal := cloneSeal(t, fixture)
			mutate(seal)
			if _, _, err := MarshalRuntimeSeal(seal); err == nil {
				t.Fatal("conflicting retained runtime accepted")
			}
		})
	}
}

func TestRuntimeSealLinkComponentsResolveWithinRetainedTree(t *testing.T) {
	seal, _ := sealFixture()
	moduleTarget := "runtime/lib.so"
	seal.Files = append(seal.Files, RuntimeFile{Path: "module", Kind: "symlink", Mode: 0777, Target: &moduleTarget})
	directoryTarget := "runtime"
	seal.Files = append(seal.Files, RuntimeFile{Path: "libdir", Kind: "symlink", Mode: 0777, Target: &directoryTarget})
	viaParent := "../libdir/lib.so"
	seal.Files[3].Target = &viaParent
	_, _, err := MarshalRuntimeSeal(seal)
	testutil.FailErr(t, "resolve link through selected directory alias", err)
	cycle := "runtime/lib-alias.so"
	seal.Files[len(seal.Files)-1].Target = &cycle
	if _, _, err := MarshalRuntimeSeal(seal); err == nil {
		t.Fatal("expanding component cycle accepted")
	}
}
