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

// minimalValidWasm is a valid empty Wasm module (0 imports) so NewService
// passes EnforceImportAudit=true without requiring a full 38 MB python.wasm.
func minimalValidWasm() []byte {
	return []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
}

func buildSyntheticServiceImage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	wasmBytes := minimalValidWasm()
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
	if err := os.MkdirAll(filepath.Join(pkgRoot, "demo-1.0.0.dist-info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgRoot, "demo", "__init__.py"), []byte("X = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgRoot, "demo-1.0.0.dist-info", "RECORD"), []byte("demo/__init__.py,,\n"), 0o644); err != nil {
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
		BuiltAt:       "2026-11-27T00:00:00Z",
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
		Distributions: []pythonruntime.DistributionSpec{
			{
				Name:           "demo",
				NormalizedName: "demo",
				Version:        "1.0.0",
				Public:         true,
				Imports:        []string{"demo"},
				DistInfoPath:   "packages/demo-1.0.0.dist-info",
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
// NewService and NewVerifiedRuntime construction independently verifies the
// image on disk even when reusing the exact same ServiceConfig / RuntimeConfig
// struct after a staged-image change (no reusable proof token or cache).
func TestNewServiceFreshVerificationNoCrossServiceCache(t *testing.T) {
	ctx := context.Background()
	imgDir := buildSyntheticServiceImage(t)

	svcCfg := ServiceConfig{
		ImageDir: imgDir,
		Backend:  pythonruntime.BackendWazeroInterpreter,
	}
	svc1, err := NewService(ctx, svcCfg)
	if err != nil {
		t.Fatalf("first NewService on clean image failed: %v", err)
	}
	rtCfg := pythonruntime.RuntimeConfig{
		Backend:            pythonruntime.BackendWazeroInterpreter,
		ImageDir:           imgDir,
		Manifest:           svc1.RuntimeManifest(),
		EnforceImportAudit: true,
	}
	if err := svc1.Close(ctx); err != nil {
		t.Fatalf("Close svc1: %v", err)
	}
	rt1, err := pythonruntime.NewVerifiedRuntime(ctx, rtCfg)
	if err != nil {
		t.Fatalf("first NewVerifiedRuntime on clean image failed: %v", err)
	}
	if err := rt1.Close(ctx); err != nil {
		t.Fatalf("Close rt1: %v", err)
	}

	// Tamper with a package file between constructions and reuse exact configs.
	if err := os.WriteFile(filepath.Join(imgDir, "packages", "demo", "__init__.py"), []byte("X = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(ctx, svcCfg); err == nil || !strings.Contains(err.Error(), "packages_root tree sha256 mismatch") {
		t.Fatalf("reusing ServiceConfig after .py tamper must fail closed, got: %v", err)
	}
	if _, err := pythonruntime.NewVerifiedRuntime(ctx, rtCfg); err == nil || !strings.Contains(err.Error(), "packages_root tree sha256 mismatch") {
		t.Fatalf("reusing RuntimeConfig with NewVerifiedRuntime after .py tamper must fail closed, got: %v", err)
	}

	// Restore .py and plant a forbidden host native binary.
	if err := os.WriteFile(filepath.Join(imgDir, "packages", "demo", "__init__.py"), []byte("X = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imgDir, "evil.so"), []byte("ELF"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(ctx, svcCfg); err == nil || !strings.Contains(err.Error(), "host native binaries forbidden") {
		t.Fatalf("reusing ServiceConfig after planting .so must fail closed, got: %v", err)
	}
	if _, err := pythonruntime.NewVerifiedRuntime(ctx, rtCfg); err == nil || !strings.Contains(err.Error(), "host native binaries forbidden") {
		t.Fatalf("reusing RuntimeConfig with NewVerifiedRuntime after planting .so must fail closed, got: %v", err)
	}
}
