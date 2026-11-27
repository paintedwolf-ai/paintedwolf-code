package pythonruntime

import (
	"context"
	"errors"
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
