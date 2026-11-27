package pythonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pythonnative"
)

// minimalNoopWasmModule is a valid Wasm 1.0 module with an empty _start export
// and a 1-page memory export, allowing wazero backend selection tests without
// needing a full CPython WASI image.
var minimalNoopWasmModule = []byte{
	0x00, 0x61, 0x73, 0x6d, // \0asm
	0x01, 0x00, 0x00, 0x00, // version 1
	// Type section: 1 type () -> ()
	0x01, 0x04, 0x01, 0x60, 0x00, 0x00,
	// Function section: 1 func of type 0
	0x03, 0x02, 0x01, 0x00,
	// Memory section: 1 memory, min 1 page
	0x05, 0x03, 0x01, 0x00, 0x01,
	// Export section: "memory" (mem 0), "_start" (func 0)
	0x07, 0x13, 0x02,
	0x06, 'm', 'e', 'm', 'o', 'r', 'y', 0x02, 0x00,
	0x06, '_', 's', 't', 'a', 'r', 't', 0x00, 0x00,
	// Code section: 1 body (empty, end)
	0x0a, 0x04, 0x01, 0x02, 0x00, 0x0b,
}

func TestLibraryNameForPlatformCrossTarget(t *testing.T) {
	t.Parallel()

	// Cross-target resolution works identically regardless of the running host.
	gotDarwin, err := LibraryNameForPlatform("darwin", "arm64")
	if err != nil || gotDarwin != "libpw_cpython_wasi.dylib" {
		t.Fatalf("LibraryNameForPlatform(darwin, arm64) = (%q, %v), want (libpw_cpython_wasi.dylib, nil)", gotDarwin, err)
	}
	gotLinux, err := LibraryNameForPlatform("linux", "amd64")
	if err != nil || gotLinux != "libpw_cpython_wasi.so" {
		t.Fatalf("LibraryNameForPlatform(linux, amd64) = (%q, %v), want (libpw_cpython_wasi.so, nil)", gotLinux, err)
	}

	// Unknown or unsupported OS/arch pairs are explicitly rejected instead of
	// defaulting to a Linux-style .so filename.
	unsupported := [][2]string{
		{"windows", "amd64"},
		{"freebsd", "amd64"},
		{"linux", "arm64"},
		{"darwin", "amd64"},
		{"plan9", "amd64"},
		{"", "amd64"},
		{"linux", ""},
		{"", ""},
	}
	for _, tc := range unsupported {
		name, err := LibraryNameForPlatform(tc[0], tc[1])
		if err == nil {
			t.Errorf("LibraryNameForPlatform(%q, %q) = (%q, nil), want explicit unsupported error", tc[0], tc[1], name)
		}
	}
}

func TestDefaultBackendKindAlwaysNativeAOTAndNoSilentFallback(t *testing.T) {
	t.Parallel()

	if got := DefaultBackendKind(); got != BackendNativeAOT {
		t.Fatalf("DefaultBackendKind() = %q, want %q unconditionally", got, BackendNativeAOT)
	}

	// Default route with no staged engine (or without cgo capability) must fail
	// closed with a NativeEngineVerificationError and never silently fall back
	// to wazero, even when valid WasmBytes are supplied.
	_, err := NewConfiguredRuntime(context.Background(), RuntimeConfig{
		WasmBytes:   minimalNoopWasmModule,
		EngineDir:   t.TempDir(),
		ScratchRoot: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected NewConfiguredRuntime with default backend to fail closed, got nil error")
	}
	var ve *NativeEngineVerificationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *NativeEngineVerificationError, got %T: %v", err, err)
	}
	if !pythonnative.NativeAOTSupported() {
		if ve.Stage != "capability" {
			t.Fatalf("expected Stage=%q when native AOT capability is unsupported, got %q (%v)", "capability", ve.Stage, err)
		}
		var capErr *pythonnative.UnsupportedCapabilityError
		if !errors.As(err, &capErr) {
			t.Fatalf("expected wrapped *pythonnative.UnsupportedCapabilityError, got %v", err)
		}
	}
}

func TestWazeroExplicitBackendSelectionReportsInstantiatedBackend(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Explicitly request wazero-interpreter on EVERY platform (including Linux)
	// and verify Info().Backend and ExecutionResult.Backend report the actual
	// instantiated backend rather than re-inferring from runtime.GOOS.
	interpRT, err := NewConfiguredRuntime(ctx, RuntimeConfig{
		Backend:     BackendWazeroInterpreter,
		WasmBytes:   minimalNoopWasmModule,
		ScratchRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewConfiguredRuntime(wazero-interpreter): %v", err)
	}
	defer func() { _ = interpRT.Close(ctx) }()

	if got := interpRT.Info().Backend; got != BackendWazeroInterpreter {
		t.Fatalf("interpRT.Info().Backend = %q, want %q", got, BackendWazeroInterpreter)
	}
	res, err := interpRT.Execute(ctx, Invocation{
		ExecutionID: "test-explicit-interp",
		Mode:        ModeCode,
		Code:        "pass",
	})
	if err != nil {
		t.Fatalf("interpRT.Execute: %v", err)
	}
	if res.Backend != BackendWazeroInterpreter {
		t.Fatalf("interpRT ExecutionResult.Backend = %q, want %q", res.Backend, BackendWazeroInterpreter)
	}

	// When the platform adapter allows wazero-compiler as the reference backend
	// (e.g. Linux amd64), verify explicit compiler selection also reports
	// BackendWazeroCompiler accurately.
	if !pythonnative.ReferenceWazeroUsesInterpreter() {
		compRT, err := NewConfiguredRuntime(ctx, RuntimeConfig{
			Backend:     BackendWazeroCompiler,
			WasmBytes:   minimalNoopWasmModule,
			ScratchRoot: t.TempDir(),
		})
		if err != nil {
			t.Fatalf("NewConfiguredRuntime(wazero-compiler): %v", err)
		}
		defer func() { _ = compRT.Close(ctx) }()

		if got := compRT.Info().Backend; got != BackendWazeroCompiler {
			t.Fatalf("compRT.Info().Backend = %q, want %q", got, BackendWazeroCompiler)
		}
		compRes, err := compRT.Execute(ctx, Invocation{
			ExecutionID: "test-explicit-compiler",
			Mode:        ModeCode,
			Code:        "pass",
		})
		if err != nil {
			t.Fatalf("compRT.Execute: %v", err)
		}
		if compRes.Backend != BackendWazeroCompiler {
			t.Fatalf("compRT ExecutionResult.Backend = %q, want %q", compRes.Backend, BackendWazeroCompiler)
		}
	}
}

func TestCrossTargetSelectionSigningAndUnknownTriples(t *testing.T) {
	t.Parallel()

	// 1. Direct adapter target triple resolution independent of running host.
	darwinSpec, err := pythonnative.ResolveTargetTriple("aarch64-apple-darwin")
	if err != nil {
		t.Fatalf("ResolveTargetTriple(aarch64-apple-darwin): %v", err)
	}
	if darwinSpec.GOOS != "darwin" || darwinSpec.GOARCH != "arm64" || darwinSpec.LibraryName != "libpw_cpython_wasi.dylib" || !darwinSpec.RequiresHardenedSigning {
		t.Fatalf("unexpected darwin target spec: %+v", darwinSpec)
	}

	linuxSpec, err := pythonnative.ResolveTargetTriple("x86_64-unknown-linux-gnu")
	if err != nil {
		t.Fatalf("ResolveTargetTriple(x86_64-unknown-linux-gnu): %v", err)
	}
	if linuxSpec.GOOS != "linux" || linuxSpec.GOARCH != "amd64" || linuxSpec.LibraryName != "libpw_cpython_wasi.so" || linuxSpec.RequiresHardenedSigning {
		t.Fatalf("unexpected linux target spec: %+v", linuxSpec)
	}

	for _, badTriple := range []string{"aarch64-unknown-linux-gnu", "x86_64-pc-windows-msvc", "freebsd/amd64", ""} {
		if got, err := pythonnative.ResolveTargetTriple(badTriple); err == nil {
			t.Errorf("ResolveTargetTriple(%q) = %+v, nil; want explicit error", badTriple, got)
		}
	}

	// 2. End-to-end SelectEngineRelease consumes adapter signing & library facts.
	commitSHA := strings.Repeat("a", 40)
	validSHA := strings.Repeat("b", 64)
	makeDesc := func(artifacts map[string]EngineReleaseDescriptorArtifact) string {
		var desc EngineReleaseDescriptor
		desc.SchemaVersion = EngineReleaseDescriptorSchemaVersion
		desc.EngineTag = "pw-cpython-wasi-engine-v0.1.0"
		desc.SourceCommit = commitSHA
		desc.ABIVersion = ExpectedNativeEngineABIVersion
		desc.ABIHeaderSHA256 = ExpectedNativeEngineABIHeaderSHA256
		desc.CPython.Version = "3.12.0"
		desc.CPython.WasmSHA256 = validSHA
		desc.Metering.MeteredWasmSHA256 = validSHA
		desc.ImportInventory.Count = ExpectedNativeEngineImportCount
		desc.ImportInventory.SHA256 = ExpectedNativeEngineImportInventorySHA256
		desc.Artifacts = artifacts
		raw, err := json.Marshal(desc)
		if err != nil {
			t.Fatalf("marshal descriptor: %v", err)
		}
		p := filepath.Join(t.TempDir(), "release-manifest.json")
		if err := os.WriteFile(p, raw, 0o644); err != nil {
			t.Fatalf("write descriptor: %v", err)
		}
		return p
	}

	// 2a. Valid cross-target descriptor: darwin hardened=true, linux hardened=false,
	// library_name omitted so adapter fills canonical filenames for both targets.
	okDesc := makeDesc(map[string]EngineReleaseDescriptorArtifact{
		"aarch64-apple-darwin": {
			Platform:         "aarch64-apple-darwin",
			ArchiveName:      "engine-darwin-arm64.tar.gz",
			LibrarySHA256:    validSHA,
			CodesignHardened: true,
		},
		"x86_64-unknown-linux-gnu": {
			Platform:         "x86_64-unknown-linux-gnu",
			ArchiveName:      "engine-linux-amd64.tar.gz",
			LibrarySHA256:    validSHA,
			CodesignHardened: false,
		},
	})
	manifest, err := SelectEngineRelease(context.Background(), SelectEngineReleaseRequest{
		DescriptorSource: okDesc,
		Tag:              "pw-cpython-wasi-engine-v0.1.0",
		ExpectedCommit:   commitSHA,
	})
	if err != nil {
		t.Fatalf("SelectEngineRelease(valid cross-target): %v", err)
	}
	dArt, err := manifest.ArtifactForPlatform("darwin", "arm64")
	if err != nil || dArt.LibraryName != "libpw_cpython_wasi.dylib" {
		t.Fatalf("darwin/arm64 artifact = (%+v, %v), want libpw_cpython_wasi.dylib", dArt, err)
	}
	lArt, err := manifest.ArtifactForPlatform("linux", "amd64")
	if err != nil || lArt.LibraryName != "libpw_cpython_wasi.so" {
		t.Fatalf("linux/amd64 artifact = (%+v, %v), want libpw_cpython_wasi.so", lArt, err)
	}

	// 2b. Unsigned darwin/arm64 artifact must be rejected on every host.
	unsignedDarwinDesc := makeDesc(map[string]EngineReleaseDescriptorArtifact{
		"aarch64-apple-darwin": {
			Platform:         "aarch64-apple-darwin",
			ArchiveName:      "engine-darwin-arm64.tar.gz",
			LibrarySHA256:    validSHA,
			CodesignHardened: false,
		},
	})
	if _, err := SelectEngineRelease(context.Background(), SelectEngineReleaseRequest{
		DescriptorSource: unsignedDarwinDesc,
		Tag:              "pw-cpython-wasi-engine-v0.1.0",
		ExpectedCommit:   commitSHA,
	}); err == nil || !strings.Contains(err.Error(), "hardened-runtime signing") {
		t.Fatalf("expected hardened-runtime signing rejection for unsigned darwin/arm64, got %v", err)
	}

	// 2c. Unknown target triple must be rejected explicitly.
	unknownTripleDesc := makeDesc(map[string]EngineReleaseDescriptorArtifact{
		"x86_64-pc-windows-msvc": {
			Platform:      "x86_64-pc-windows-msvc",
			ArchiveName:   "engine-windows-amd64.tar.gz",
			LibrarySHA256: validSHA,
		},
	})
	if _, err := SelectEngineRelease(context.Background(), SelectEngineReleaseRequest{
		DescriptorSource: unknownTripleDesc,
		Tag:              "pw-cpython-wasi-engine-v0.1.0",
		ExpectedCommit:   commitSHA,
	}); err == nil || !strings.Contains(err.Error(), "unsupported portable python native engine target") {
		t.Fatalf("expected unsupported target error for x86_64-pc-windows-msvc, got %v", err)
	}
}
