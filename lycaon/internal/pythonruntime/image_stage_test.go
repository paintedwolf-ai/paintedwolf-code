package pythonruntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func buildImageArchiveTarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %s: %v", name, err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatalf("write tar body %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestEmbeddedBundledManifestPinsImageAndEngine(t *testing.T) {
	m, err := LoadBundledEngineManifest()
	if err != nil {
		t.Fatalf("LoadBundledEngineManifest failed: %v", err)
	}
	if err := ValidateBundledEngineManifest(m); err != nil {
		t.Fatalf("ValidateBundledEngineManifest failed: %v", err)
	}
	if m.Image == nil {
		t.Fatal("embedded bundled-manifest.yaml must pin portable_python_image")
	}
	if m.Image.BytecodeInvalidationMode != "checked-hash" || m.Image.PycCount <= 0 {
		t.Fatalf("unexpected bundled image bytecode spec: %+v", m.Image)
	}
}

func TestStageImageArtifactPurgesStaleAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	work := t.TempDir()

	// Build a minimal valid checked-hash CPython 3.12 image directory first to compute exact hashes.
	srcImg := filepath.Join(work, "src-image")
	for _, sub := range []string{"bin", "lib", "packages/__pycache__"} {
		if err := os.MkdirAll(filepath.Join(srcImg, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	wasmBytes := []byte("fake-cpython-wasm")
	siteBytes := []byte("# stdlib\n")
	pkgBytes := []byte("X = 1\n")
	validPyc := []byte{
		0xcb, 0x0d, 0x0d, 0x0a,
		0x03, 0x00, 0x00, 0x00,
		0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88,
		0xe3, 0x00, 0x00, 0x00,
	}
	if err := os.WriteFile(filepath.Join(srcImg, "bin", "python.wasm"), wasmBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcImg, "lib", "site.py"), siteBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcImg, "packages", "mod.py"), pkgBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcImg, "packages", "__pycache__", "mod.cpython-312.pyc"), validPyc, 0o644); err != nil {
		t.Fatal(err)
	}

	wasmSHA := sha256Hex(wasmBytes)
	meteredSHA := "774a08a7863c1dee1b2f21bb80fb9964f0dd5dc27bdf2294bdd8a11934e893c6"
	stdlibSHA, _ := HashTree(filepath.Join(srcImg, "lib"))
	pkgSHA, _ := HashTree(filepath.Join(srcImg, "packages"))
	pycTreeSHA, _ := hashRelativeFiles(srcImg, []string{"packages/__pycache__/mod.cpython-312.pyc"})

	im := ImageManifest{
		SchemaVersion: ImageManifestSchemaVersion,
		ImageID:       "pp-cpython-3.12.0-wasi-testpyc",
		Interpreter: InterpreterSpec{
			Name:              "cpython",
			Version:           "3.12.0",
			WasmPath:          "bin/python.wasm",
			WasmSHA256:        wasmSHA,
			MeteredWasmSHA256: meteredSHA,
			StdlibPath:        "lib",
			StdlibSHA256:      stdlibSHA,
		},
		PackagesRoot:          "packages",
		PackagesTreeSHA256:    pkgSHA,
		ImportInventorySHA256: ExpectedNativeEngineImportInventorySHA256,
		Bytecode: &BytecodeSpec{
			InvalidationMode: "checked-hash",
			CPythonVersion:   "3.12",
			MagicHex:         CPython312BytecodeMagicHex,
			PycCount:         1,
			PycTreeSHA256:    pycTreeSHA,
		},
	}
	manBytes, err := json.MarshalIndent(im, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manSHA := sha256Hex(manBytes)

	archiveBytes := buildImageArchiveTarGz(t, map[string][]byte{
		"image/bin/python.wasm":                                wasmBytes,
		"image/lib/site.py":                                    siteBytes,
		"image/packages/mod.py":                                pkgBytes,
		"image/packages/__pycache__/mod.cpython-312.pyc":       validPyc,
		"image/image-manifest.json":                            manBytes,
	})
	devDir := filepath.Join(work, "dev-artifacts")
	if err := os.MkdirAll(filepath.Join(devDir, "PY08", "image"), 0o755); err != nil {
		t.Fatal(err)
	}
	archiveName := "portable-python-image-pyc.tar.gz"
	if err := os.WriteFile(filepath.Join(devDir, "PY08", "image", archiveName), archiveBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	m := &BundledEngineManifest{
		Engine: EngineReleaseSpec{
			Version:               "3.12.0+paintedwolf.1",
			Origin:                "downstream",
			UpstreamVersion:       "3.12.0",
			BaseRevision:          "c8dc215cbd465623592b996f56c394124053d3b2",
			Revision:              1,
			ABIVersion:            ExpectedNativeEngineABIVersion,
			ABIHeaderSHA256:       ExpectedNativeEngineABIHeaderSHA256,
			CPythonWasmSHA256:     wasmSHA,
			MeteredWasmSHA256:     meteredSHA,
			ImportInventoryCount:  ExpectedNativeEngineImportCount,
			ImportInventorySHA256: ExpectedNativeEngineImportInventorySHA256,
			License:               "PSF-2.0 AND Apache-2.0",
			Artifacts: []EngineReleaseArtifact{
				{
					GOOS:        "linux",
					GOARCH:      "amd64",
					LibraryName: "libpw_cpython_wasi.so",
					URL:         "https://example.invalid/lib.tar.gz",
					SHA256:      "94fb1994107f9fa0396e740cdc5cc3cdaaf73f3807df41de000df4eba9cd80b4",
				},
			},
		},
		Image: &BundledImageSpec{
			ImageID:                  im.ImageID,
			ArchiveName:              archiveName,
			ArchiveSHA256:            sha256Hex(archiveBytes),
			ArchiveBytes:             int64(len(archiveBytes)),
			ManifestSHA256:           manSHA,
			PackagesTreeSHA256:       pkgSHA,
			CPythonWasmSHA256:        wasmSHA,
			MeteredWasmSHA256:        meteredSHA,
			ImportInventorySHA256:    ExpectedNativeEngineImportInventorySHA256,
			BytecodeInvalidationMode: "checked-hash",
			BytecodeMagicHex:         CPython312BytecodeMagicHex,
			PycCount:                 1,
			PycTreeSHA256:            pycTreeSHA,
			URL:                      "https://example.invalid/portable-python-image-pyc.tar.gz",
		},
	}

	// Pre-populate a stale file in <stageRoot>/portable-python/image to prove clean replacement.
	stageRoot := filepath.Join(work, "stage-root")
	staleFile := filepath.Join(StagedImageDir(stageRoot), "stale-from-old-version.txt")
	if err := os.MkdirAll(filepath.Dir(staleFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staleFile, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	stagedDir, err := StageImageArtifact(ctx, m, "", stageRoot, devDir, nil)
	if err != nil {
		t.Fatalf("StageImageArtifact failed: %v", err)
	}
	if _, err := os.Stat(staleFile); !os.IsNotExist(err) {
		t.Fatalf("expected stale file to be purged on replacement, stat err=%v", err)
	}
	if _, err := VerifyStagedImageArtifact(m, stageRoot); err != nil {
		t.Fatalf("VerifyStagedImageArtifact failed: %v", err)
	}

	// Tampering staged image-manifest.json must fail VerifyStagedImageArtifact.
	if err := os.WriteFile(filepath.Join(stagedDir, "packages", "extra-stale.py"), []byte("bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyStagedImageArtifact(m, stageRoot); err == nil {
		t.Fatal("expected VerifyStagedImageArtifact to fail on extra stale file in packages/")
	}

	// Missing image source with no dev artifact and invalid URL must fail closed.
	mMissing := *m
	imgCopy := *m.Image
	imgCopy.ArchiveName = "nonexistent-image-archive.tar.gz"
	imgCopy.URL = ""
	mMissing.Image = &imgCopy
	if _, err := StageImageArtifact(ctx, &mMissing, "", filepath.Join(work, "missing-stage"), filepath.Join(work, "empty"), nil); err == nil {
		t.Fatal("expected StageImageArtifact to fail closed when image archive is missing")
	}
}
