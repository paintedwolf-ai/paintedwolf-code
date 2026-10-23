package packagegeneration

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func sealFile(name, content string) RuntimeFile {
	digest, size := runtimeDigest([]byte(content)), int64(len(content))
	return RuntimeFile{Path: name, Kind: "file", Mode: 0644, SHA256: &digest, Size: &size}
}

func sealFixture() (*RuntimeSeal, []byte) {
	base := []byte(`{"format_version":2,"package":"painted-wolf-code","version":"1.0.2","architecture":"x86_64","install_root":"/opt/painted-wolf-code","executables":[{"path":"/opt/painted-wolf-code/bin/desktop","sha256":"` + runtimeDigest([]byte("x")) + `","size":1}],"payload":[{"path":"bin","kind":"directory","mode":493,"sha256":null,"size":null,"target":null},{"path":"bin/desktop","kind":"file","mode":493,"sha256":"` + runtimeDigest([]byte("x")) + `","size":1,"target":null}]}`)
	module := sealFile("runtime/lib.so", "module")
	original := sealFile("inputs/helper", "original helper")
	anchor := sealFile("runtime/helper", "guarded helper")
	inventory := sealFile("inventory.bin", "inventory")
	link := "lib.so"
	return &RuntimeSeal{
		FormatVersion: RuntimeSealVersion, BaseManifestSHA256: runtimeDigest(base), Architecture: "x86_64",
		InventoryPath: inventory.Path, InventorySHA256: *inventory.SHA256, ProducerVersion: "runtime-v1",
		Files: []RuntimeFile{
			{Path: "runtime", Kind: "directory", Mode: 0755}, module, anchor,
			{Path: "runtime/lib-alias.so", Kind: "symlink", Mode: 0777, Target: &link},
			{Path: "inputs", Kind: "directory", Mode: 0755}, original, inventory,
		},
		Sources: []RuntimeSource{
			{Path: "/usr/lib/lib.so", CapturedPath: module.Path, SHA256: *module.SHA256, Size: *module.Size,
				Origin: RuntimeOrigin{Kind: "native", SelectedRoot: "/usr/lib"}},
			{Path: "/usr/lib/helper", CapturedPath: original.Path, SHA256: *original.SHA256, Size: *original.Size,
				Origin: RuntimeOrigin{Kind: "native", SelectedRoot: "/usr/lib"}},
		},
		Aliases: []RuntimeAlias{{Path: "/usr/lib", Target: "runtime", Kind: "directory"},
			{Path: "/usr/lib/helper", Target: anchor.Path, Kind: "file"}},
		Anchors: []RuntimeAnchor{{Role: "webkit-web", Path: anchor.Path, SHA256: *anchor.SHA256, Size: *anchor.Size}},
	}, base
}

func cloneSeal(t *testing.T, seal *RuntimeSeal) *RuntimeSeal {
	t.Helper()
	raw, err := json.Marshal(seal)
	testutil.FailErr(t, "marshal fixture", err)
	var result RuntimeSeal
	testutil.FailErr(t, "clone fixture", json.Unmarshal(raw, &result))
	return &result
}

func TestRuntimeSealCanonicalIdentityAndOriginalManifest(t *testing.T) {
	seal, base := sealFixture()
	originalFirst := seal.Files[0].Path
	raw, identity, err := MarshalRuntimeSeal(seal)
	testutil.FailErr(t, "encode seal", err)
	if seal.Files[0].Path != originalFirst || !bytes.HasSuffix(raw, []byte("\n")) || identity != runtimeDigest(raw) {
		t.Fatal("canonical encoding mutated producer order or did not bind exact newline bytes")
	}
	decoded, retained, decodedIdentity, err := ReadRuntimeSeal(bytes.NewReader(raw))
	testutil.FailErr(t, "read canonical seal", err)
	if !bytes.Equal(retained, raw) || decodedIdentity != identity {
		t.Fatal("reader changed retained bytes or identity")
	}
	testutil.FailErr(t, "verify original publisher seal", decoded.VerifyBaseManifest(base))
	if err := decoded.VerifyBaseManifest(append(base, '\n')); err == nil {
		t.Fatal("equivalent publisher JSON with different exact bytes accepted")
	}
	decoded.Architecture = "aarch64"
	if err := decoded.VerifyBaseManifest(base); err == nil {
		t.Fatal("publisher architecture mismatch accepted")
	}
}

func TestRuntimeSealRefusesAlternateJSONIdentities(t *testing.T) {
	seal, _ := sealFixture()
	raw, _, err := MarshalRuntimeSeal(seal)
	testutil.FailErr(t, "encode fixture", err)
	cases := map[string][]byte{
		"unknown field":          bytes.Replace(raw, []byte(`{"format_version":1`), []byte(`{"extra":1,"format_version":1`), 1),
		"unknown version":        bytes.Replace(raw, []byte(`"format_version":1`), []byte(`"format_version":2`), 1),
		"duplicate field":        bytes.Replace(raw, []byte(`"architecture":"x86_64"`), []byte(`"architecture":"aarch64","architecture":"x86_64"`), 1),
		"missing nullable field": bytes.Replace(raw, []byte(`,"observation":null`), nil, 1),
		"trailing object":        append(append([]byte{}, raw...), []byte("{}")...),
		"extra whitespace":       append([]byte(" "), raw...),
		"missing newline":        bytes.TrimSuffix(raw, []byte("\n")),
		"noncanonical escaping":  bytes.Replace(raw, []byte(`"x86_64"`), []byte(`"\u007886_64"`), 1),
	}
	for name, candidate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := ReadRuntimeSeal(bytes.NewReader(candidate)); err == nil {
				t.Fatal("noncanonical or unsupported document accepted")
			}
		})
	}
	seal.Files[0], seal.Files[1] = seal.Files[1], seal.Files[0]
	unsorted, err := json.Marshal(seal)
	testutil.FailErr(t, "marshal unsorted producer", err)
	if _, _, _, err := ReadRuntimeSeal(bytes.NewReader(append(unsorted, '\n'))); err == nil {
		t.Fatal("unsorted arrays accepted as a second serialized identity")
	}
}

func TestRuntimeSealNixSourceHasPortableIdentityWithoutHostInode(t *testing.T) {
	seal, _ := sealFixture()
	seal.Sources[0].Path = "/nix/store/selected-output/lib.so"
	seal.Sources[0].Origin = RuntimeOrigin{Kind: "nix", StoreRoot: "/nix/store",
		SelectedRoot: "/nix/store/selected-output", OutputPath: "/nix/store/selected-output"}
	_, _, err := MarshalRuntimeSeal(seal)
	testutil.FailErr(t, "encode portable Nix source without observation", err)
	seal.Sources[0].Observation = &KernelObservation{Device: 99, Inode: 42, UID: 65534}
	_, _, err = MarshalRuntimeSeal(seal)
	testutil.FailErr(t, "retain diagnostic projected ownership", err)
	seal.Sources[0].Origin.OutputPath = "/nix/store/another-output"
	if _, _, err := MarshalRuntimeSeal(seal); err == nil {
		t.Fatal("selected Nix output mismatch accepted")
	}
}

func TestRuntimeSealMemoryAndEntryCeilings(t *testing.T) {
	if _, _, _, err := ReadRuntimeSeal(strings.NewReader(strings.Repeat(" ", RuntimeSealMaxBytes+1))); err == nil {
		t.Fatal("oversized input accepted")
	}
	seal, _ := sealFixture()
	seal.Files = make([]RuntimeFile, RuntimeSealMaxEntries)
	if _, _, err := MarshalRuntimeSeal(seal); err == nil {
		t.Fatal("combined entry ceiling ignored")
	}
}
