package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

func makeTarGz(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sumHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestPortablePythonArtifactCLIRoundTrip(t *testing.T) {
	dir := t.TempDir()
	darwinLib := []byte("cli-test-darwin-dylib")
	linuxLib := []byte("cli-test-linux-so")
	darwinArc := makeTarGz(t, "libpw_cpython_wasi.dylib", darwinLib)
	linuxArc := makeTarGz(t, "libpw_cpython_wasi.so", linuxLib)

	if err := os.WriteFile(filepath.Join(dir, "darwin.tar.gz"), darwinArc, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "linux.tar.gz"), linuxArc, 0o644); err != nil {
		t.Fatal(err)
	}

	commit := "914f9f69fa94f5f2d84182808d4f989b74f070b9"
	var desc pythonruntime.EngineReleaseDescriptor
	desc.SchemaVersion = pythonruntime.EngineReleaseDescriptorSchemaVersion
	desc.EngineTag = "pw-cpython-wasi-engine-v0.1.0"
	desc.SourceCommit = commit
	desc.ABIVersion = pythonruntime.ExpectedNativeEngineABIVersion
	desc.ABIHeaderSHA256 = pythonruntime.ExpectedNativeEngineABIHeaderSHA256
	desc.CPython.Version = "3.12.0"
	desc.CPython.WasmSHA256 = "5ce0cbeb843e6e5abf2d50c7158002e8333c26a40fbe27a7a52e66ee48cf64a8"
	desc.Metering.MeteredWasmSHA256 = "774a08a7863c1dee1b2f21bb80fb9964f0dd5dc27bdf2294bdd8a11934e893c6"
	desc.ImportInventory.Count = pythonruntime.ExpectedNativeEngineImportCount
	desc.ImportInventory.SHA256 = pythonruntime.ExpectedNativeEngineImportInventorySHA256
	desc.Artifacts = map[string]pythonruntime.EngineReleaseDescriptorArtifact{
		"darwin-arm64": {
			Platform:         "darwin-arm64",
			ArchiveName:      "darwin.tar.gz",
			ArchiveSHA256:    sumHex(darwinArc),
			LibraryName:      "libpw_cpython_wasi.dylib",
			LibrarySHA256:    sumHex(darwinLib),
			LibraryBytes:     int64(len(darwinLib)),
			CodesignHardened: true,
			JITEntitlements:  []string{},
		},
		"linux-amd64": {
			Platform:         "linux-amd64",
			ArchiveName:      "linux.tar.gz",
			ArchiveSHA256:    sumHex(linuxArc),
			LibraryName:      "libpw_cpython_wasi.so",
			LibrarySHA256:    sumHex(linuxLib),
			LibraryBytes:     int64(len(linuxLib)),
			CodesignHardened: false,
			JITEntitlements:  []string{},
		},
	}

	raw, err := json.MarshalIndent(desc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	descPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(descPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(dir, "bundled-manifest.yaml")
	cacheDir := filepath.Join(dir, "cache")
	stageRoot := filepath.Join(dir, "engine-root")

	if err := run([]string{
		"-mode", "select",
		"-manifest", manifestPath,
		"-descriptor", descPath,
		"-tag", desc.EngineTag,
		"-expected-commit", commit,
	}); err != nil {
		t.Fatalf("select failed: %v", err)
	}

	for _, target := range []string{"aarch64-apple-darwin", "x86_64-unknown-linux-gnu"} {
		if err := run([]string{"-mode", "resolve", "-manifest", manifestPath, "-cache-dir", cacheDir, "-target", target}); err != nil {
			t.Fatalf("resolve(%s) failed: %v", target, err)
		}
		if err := run([]string{"-mode", "stage", "-manifest", manifestPath, "-cache-dir", cacheDir, "-root", stageRoot, "-target", target}); err != nil {
			t.Fatalf("stage(%s) failed: %v", target, err)
		}
		if err := run([]string{"-mode", "verify", "-manifest", manifestPath, "-root", stageRoot, "-target", target}); err != nil {
			t.Fatalf("verify(%s) failed: %v", target, err)
		}
		if err := run([]string{"-mode", "identity", "-manifest", manifestPath, "-target", target}); err != nil {
			t.Fatalf("identity(%s) failed: %v", target, err)
		}
	}
}
