//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/scan/onefile"
	"github.com/lycaon/lycaon/internal/testutil"
)

func relocationDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func relocationELF() []byte {
	raw := make([]byte, 120)
	copy(raw, []byte{'\x7f', 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(raw[16:], 3)
	machine := uint16(62)
	if runtime.GOARCH == "arm64" {
		machine = 183
	}
	binary.LittleEndian.PutUint16(raw[18:], machine)
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint64(raw[32:], 64)
	binary.LittleEndian.PutUint16(raw[52:], 64)
	binary.LittleEndian.PutUint16(raw[54:], 56)
	binary.LittleEndian.PutUint16(raw[56:], 1)
	binary.LittleEndian.PutUint32(raw[64:], 2)
	return raw
}

func relocationFixture(t *testing.T, invalidImage bool) (*bundled.Manifest, options, bundled.ConsumerRecipe) {
	t.Helper()
	source, err := bundled.LoadManifest()
	testutil.FailErr(t, "load fixture source anchors", err)
	source = &bundled.Manifest{OpenGrep: source.OpenGrep}
	var body bytes.Buffer
	testutil.FailErr(t, "encode fixture attached image", onefile.Write(&body, &onefile.Image{Launcher: relocationELF(), Members: []onefile.Member{
		{Name: "program.bin", Executable: true, Data: relocationELF()}, {Name: "data/file", Data: []byte("preserved")},
	}}))
	if invalidImage {
		body.Reset()
		body.WriteString("invalid attached image")
	}
	root := t.TempDir()
	flat := bundled.EngineBundledDir(root, source.OpenGrep.Version)
	testutil.FailErr(t, "create fixture stage", os.MkdirAll(flat, 0o700))
	testutil.FailErr(t, "stage fixture executable", os.WriteFile(filepath.Join(flat, "opengrep"), body.Bytes(), 0o700))
	identity := bundled.BuildIdentity{SchemaVersion: 1, Version: source.OpenGrep.Version, SourceManifestSHA256: source.OpenGrep.SourceManifestSHA256,
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, BinarySHA256: relocationDigest(body.Bytes()), BinaryBytes: int64(body.Len())}
	for _, name := range []string{"opengrep-source.tar.gz", "source-lock.json", "provenance.json", "LICENSE", "contracts.jsonl", "platform-checks.json", "runtime.json", "NOTICES-opengrep.md"} {
		raw := []byte("fixture " + name)
		part := bundled.PayloadIdentity{Name: name, SHA256: relocationDigest(raw), Bytes: int64(len(raw))}
		identity.Payload = append(identity.Payload, part)
		if name == "source-lock.json" {
			source.OpenGrep.SourceLockSHA256, identity.SourceLockSHA256 = part.SHA256, part.SHA256
		}
		testutil.FailErr(t, "stage fixture producer payload", os.WriteFile(filepath.Join(flat, name), raw, 0o600))
	}
	raw, err := json.Marshal(identity)
	testutil.FailErr(t, "encode fixture selected authority", err)
	manifest, err := bundled.ManifestWithBuildIdentity(source, base64.StdEncoding.EncodeToString(raw))
	testutil.FailErr(t, "bind fixture selected authority", err)
	tool := filepath.Join(t.TempDir(), "patch-fixture")
	script := []byte("#!/bin/sh\nfor last do :; done\nprintf '.patched' >> \"$last\"\n")
	testutil.FailErr(t, "write deterministic tool fixture", os.WriteFile(tool, script, 0o700))
	recipe := bundled.ConsumerRecipe{SchemaVersion: 1, Kind: "nix-nuitka-relocation", SourceCommit: strings.Repeat("a", 40), FlakeLockSHA256: strings.Repeat("b", 64),
		ToolPath: tool, ToolSHA256: relocationDigest(script), Arguments: []string{"--set-interpreter", "/nix/store/fixture-glibc/lib/ld.so", "--set-rpath", "/nix/store/fixture-glibc/lib"}}
	return manifest, options{root: root, directory: t.TempDir()}, recipe
}

func TestConsumerRelocationIsAtomicAndDoesNotGrantQualification(t *testing.T) {
	manifest, opts, recipe := relocationFixture(t, false)
	testutil.FailErr(t, "rewrite launcher and embedded fixture ELFs", relocateConsumer(t.Context(), manifest, opts, recipe))
	path := bundled.ArtifactPath(opts.root, manifest.OpenGrep.Version, runtime.GOOS)
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read rewritten fixture", err)
	image, err := onefile.Read(raw)
	testutil.FailErr(t, "validate rewritten checksums and trailer", err)
	if !bytes.HasSuffix(image.Launcher, []byte(".patched")) || !bytes.HasSuffix(image.Members[0].Data, []byte(".patched")) || string(image.Members[1].Data) != "preserved" {
		t.Fatal("relocation changed data or omitted an executable image")
	}
	if _, err := bundled.VerifyStagedArtifact(manifest, opts.root, runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("rewritten bytes retained the original shipping identity")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "consumer-finalization.json")); !os.IsNotExist(err) {
		t.Fatal("relocation alone emitted qualification authority")
	}
	raw, err = os.ReadFile(filepath.Join(filepath.Dir(path), "platform-checks.json"))
	testutil.FailErr(t, "read preserved producer proof", err)
	if string(raw) != "fixture platform-checks.json" {
		t.Fatal("relocation changed original producer proof")
	}
}

func TestConsumerRelocationRefusalsDoNotReplaceOriginal(t *testing.T) {
	for _, name := range []string{"wrong kind", "extra argument", "host loader", "host RPATH", "relative root", "same original", "wrong tool hash", "missing tool", "invalid image", "canceled"} {
		t.Run(name, func(t *testing.T) {
			manifest, opts, recipe := relocationFixture(t, name == "invalid image")
			binary := bundled.ArtifactPath(opts.root, manifest.OpenGrep.Version, runtime.GOOS)
			before, err := os.ReadFile(binary)
			testutil.FailErr(t, "retain original staged bytes", err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch name {
			case "wrong kind":
				recipe.Kind = "nix-elf-relocation"
			case "extra argument":
				recipe.Arguments = append(recipe.Arguments, "--other")
			case "host loader":
				recipe.Arguments[1] = "/lib64/ld.so"
			case "host RPATH":
				recipe.Arguments[3] = "/usr/lib"
			case "relative root":
				opts.root = "relative"
			case "same original":
				opts.directory = filepath.Dir(binary)
			case "wrong tool hash":
				recipe.ToolSHA256 = strings.Repeat("f", 64)
			case "missing tool":
				recipe.ToolPath = filepath.Join(t.TempDir(), "missing")
			case "canceled":
				cancel()
			}
			if err := relocateConsumer(ctx, manifest, opts, recipe); err == nil {
				t.Fatal("invalid consumer relocation admitted")
			}
			after, err := os.ReadFile(binary)
			testutil.FailErr(t, "inspect refused staged bytes", err)
			if !bytes.Equal(before, after) {
				t.Fatal("refused relocation replaced the original executable")
			}
		})
	}
}

func TestConsumerDependenciesRequireDeclaredProviders(t *testing.T) {
	relocator := consumerELFRelocator{members: map[string]onefile.Member{
		"lib.so": {Data: relocationELF()}, "sub/alias.so": {Link: "../lib.so"}, "cycle": {Link: "cycle"},
	}}
	for _, provider := range []string{"$ORIGIN/..", "$ORIGIN/../"} {
		if !relocator.dependencyPresent("sub/program", "lib.so", []string{provider}) {
			t.Fatal("retained relative library provider was lost")
		}
	}
	if !relocator.dependencyPresent("sub/program", "alias.so", []string{"$ORIGIN"}) {
		t.Fatal("internal library symlink was lost")
	}
	for _, dependency := range []string{"missing", "../lib.so", "/lib.so", "cycle"} {
		if relocator.dependencyPresent("program", dependency, []string{"$ORIGIN"}) {
			t.Fatal("undeclared or cyclic dependency admitted")
		}
	}
}

func TestConsumerRelativeLibrarySearchCannotEscapePayload(t *testing.T) {
	for _, entry := range []string{"$ORIGIN", "$ORIGIN/..", "$ORIGIN/../lib"} {
		if !consumerRelativeLibraryPath("sub/program", entry) {
			t.Fatalf("safe relative path refused: %s", entry)
		}
	}
	for _, entry := range []string{"", "/usr/lib", "$ORIGINALLY", "$ORIGIN/../..", "$ORIGIN/../../outside"} {
		if consumerRelativeLibraryPath("sub/program", entry) {
			t.Fatalf("escaping or ambient path admitted: %s", entry)
		}
	}
	if consumerRelativeLibraryPath("opengrep", "$ORIGIN/..") {
		t.Fatal("launcher escaped package root")
	}
	var output relocationToolOutput
	raw := bytes.Repeat([]byte{'x'}, 1<<20)
	for range 2 {
		n, err := output.Write(raw)
		if err != nil || n != len(raw) || len(output.data) != 4096 {
			t.Fatal("patch-tool diagnostic exceeded bound or interrupted drainage")
		}
	}
}
