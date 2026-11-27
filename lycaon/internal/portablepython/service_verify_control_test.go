package portablepython

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

// minimalWASIProcExitWasm is a valid 1-import Wasm module importing
// wasi_snapshot_preview1.proc_exit (matching pythonruntime's DefaultWASIImportPolicy).
func minimalWASIProcExitWasm() []byte {
	encodeU32 := func(v uint32) []byte {
		var out []byte
		for {
			b := byte(v & 0x7f)
			v >>= 7
			if v != 0 {
				b |= 0x80
			}
			out = append(out, b)
			if v == 0 {
				break
			}
		}
		return out
	}
	section := func(id byte, payload []byte) []byte {
		out := []byte{id}
		out = append(out, encodeU32(uint32(len(payload)))...)
		return append(out, payload...)
	}
	str := func(s string) []byte {
		out := encodeU32(uint32(len(s)))
		return append(out, []byte(s)...)
	}

	// Type 0: (i32) -> ()
	typePayload := []byte{0x01, 0x60, 0x01, 0x7f, 0x00}
	// Import: wasi_snapshot_preview1.proc_exit (func type 0)
	importPayload := []byte{0x01}
	importPayload = append(importPayload, str("wasi_snapshot_preview1")...)
	importPayload = append(importPayload, str("proc_exit")...)
	importPayload = append(importPayload, 0x00, 0x00)

	wasm := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	wasm = append(wasm, section(1, typePayload)...)
	wasm = append(wasm, section(2, importPayload)...)
	return wasm
}

func buildSyntheticServiceImage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	wasmBytes := minimalWASIProcExitWasm()
	if err := os.WriteFile(filepath.Join(dir, "python.wasm"), wasmBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	stdlibDir := filepath.Join(dir, "stdlib")
	if err := os.MkdirAll(stdlibDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stdlibDir, "os.py"), []byte("NAME = 'wasi'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgRoot := filepath.Join(dir, "packages")
	if err := os.MkdirAll(filepath.Join(pkgRoot, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgRoot, "demo", "__init__.py"), []byte("X = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wasmSum := sha256.Sum256(wasmBytes)
	stdlibSHA, err := pythonruntime.HashTree(stdlibDir)
	if err != nil {
		t.Fatal(err)
	}
	pkgSHA, err := pythonruntime.HashTree(pkgRoot)
	if err != nil {
		t.Fatal(err)
	}

	im := pythonruntime.ImageManifest{
		SchemaVersion: pythonruntime.ImageManifestSchemaVersion,
		ImageID:       "pp-cpython-3.12.0-wasi-fresh-control",
		Target:        "wasm32-wasi",
		Interpreter: pythonruntime.InterpreterSpec{
			Name:         "cpython",
			Version:      "3.12.0",
			WasmPath:     "python.wasm",
			WasmSHA256:   hex.EncodeToString(wasmSum[:]),
			StdlibPath:   "stdlib",
			StdlibSHA256: stdlibSHA,
		},
		PackagesRoot:       "packages",
		PackagesTreeSHA256: pkgSHA,
		WasmImports: []pythonruntime.WasmImportSpec{
			{
				Module:      "wasi_snapshot_preview1",
				Name:        "proc_exit",
				Kind:        "function",
				Disposition: "allow",
			},
		},
	}
	raw, err := json.MarshalIndent(im, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "image-manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestNewServiceFreshVerificationNoCrossServiceCache proves that each fresh
// NewService construction independently verifies the image on disk (no
// cross-service trusted cache or mutable-path shortcut).
func TestNewServiceFreshVerificationNoCrossServiceCache(t *testing.T) {
	ctx := context.Background()
	imgDir := buildSyntheticServiceImage(t)

	svc1, err := NewService(ctx, ServiceConfig{
		ImageDir: imgDir,
		Backend:  pythonruntime.BackendWazeroInterpreter,
	})
	if err != nil {
		t.Fatalf("first NewService on clean image failed: %v", err)
	}
	if err := svc1.Close(ctx); err != nil {
		t.Fatalf("Close svc1: %v", err)
	}

	// Tamper with a package file between Service constructions.
	if err := os.WriteFile(filepath.Join(imgDir, "packages", "demo", "__init__.py"), []byte("X = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(ctx, ServiceConfig{
		ImageDir: imgDir,
		Backend:  pythonruntime.BackendWazeroInterpreter,
	}); err == nil || !strings.Contains(err.Error(), "packages_root tree sha256 mismatch") {
		t.Fatalf("second NewService after .py tamper must fail closed, got: %v", err)
	}

	// Restore .py and plant a forbidden host native binary.
	if err := os.WriteFile(filepath.Join(imgDir, "packages", "demo", "__init__.py"), []byte("X = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imgDir, "evil.so"), []byte("ELF"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(ctx, ServiceConfig{
		ImageDir: imgDir,
		Backend:  pythonruntime.BackendWazeroInterpreter,
	}); err == nil || !strings.Contains(err.Error(), "host native binaries forbidden") {
		t.Fatalf("NewService after planting .so must fail closed, got: %v", err)
	}
}
