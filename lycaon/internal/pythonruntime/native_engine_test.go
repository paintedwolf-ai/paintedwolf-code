package pythonruntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func buildTestTarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{
		Name: name,
		Mode: 0o755,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("write tar body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

type engineTestFixture struct {
	releaseDir     string
	descriptorPath string
	tag            string
	commit         string
	cpythonSHA     string
	meteredSHA     string
	darwinLibSHA   string
	linuxLibSHA    string
}

func createEngineTestFixture(t *testing.T) engineTestFixture {
	t.Helper()
	dir := t.TempDir()
	darwinLib := []byte("fake-macho-arm64-libpw_cpython_wasi-payload")
	linuxLib := []byte("fake-elf-amd64-libpw_cpython_wasi-payload")

	darwinArc := buildTestTarGz(t, "lib/libpw_cpython_wasi.dylib", darwinLib)
	linuxArc := buildTestTarGz(t, "lib/libpw_cpython_wasi.so", linuxLib)

	darwinArcName := "pw-cpython-wasi-engine-v0.1.0-darwin-arm64.tar.gz"
	linuxArcName := "pw-cpython-wasi-engine-v0.1.0-linux-amd64.tar.gz"
	if err := os.WriteFile(filepath.Join(dir, darwinArcName), darwinArc, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, linuxArcName), linuxArc, 0o644); err != nil {
		t.Fatal(err)
	}

	commit := "914f9f69fa94f5f2d84182808d4f989b74f070b9"
	cpythonSHA := "5ce0cbeb843e6e5abf2d50c7158002e8333c26a40fbe27a7a52e66ee48cf64a8"
	meteredSHA := "774a08a7863c1dee1b2f21bb80fb9964f0dd5dc27bdf2294bdd8a11934e893c6"

	var desc EngineReleaseDescriptor
	desc.SchemaVersion = EngineReleaseDescriptorSchemaVersion
	desc.EngineTag = "pw-cpython-wasi-engine-v0.1.0"
	desc.SourceCommit = commit
	desc.ABIVersion = ExpectedNativeEngineABIVersion
	desc.ABIHeaderSHA256 = ExpectedNativeEngineABIHeaderSHA256
	desc.CPython.Version = "3.12.0"
	desc.CPython.ReleaseTag = "python/3.12.0+20231211-040d5a6"
	desc.CPython.WasiSDK = "20.0"
	desc.CPython.WasmSHA256 = cpythonSHA
	desc.Metering.AlgorithmVersion = "pw-loop-func-i64/v1"
	desc.Metering.MeteredWasmSHA256 = meteredSHA
	desc.ImportInventory.Count = ExpectedNativeEngineImportCount
	desc.ImportInventory.SHA256 = ExpectedNativeEngineImportInventorySHA256
	desc.Artifacts = map[string]EngineReleaseDescriptorArtifact{
		"darwin-arm64": {
			Platform:         "darwin-arm64",
			ArchiveName:      darwinArcName,
			ArchiveSHA256:    sha256Hex(darwinArc),
			ArchiveBytes:     int64(len(darwinArc)),
			LibraryName:      "libpw_cpython_wasi.dylib",
			LibrarySHA256:    sha256Hex(darwinLib),
			LibraryBytes:     int64(len(darwinLib)),
			CodesignHardened: true,
			JITEntitlements:  []string{},
		},
		"linux-amd64": {
			Platform:         "linux-amd64",
			ArchiveName:      linuxArcName,
			ArchiveSHA256:    sha256Hex(linuxArc),
			ArchiveBytes:     int64(len(linuxArc)),
			LibraryName:      "libpw_cpython_wasi.so",
			LibrarySHA256:    sha256Hex(linuxLib),
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
	return engineTestFixture{
		releaseDir:     dir,
		descriptorPath: descPath,
		tag:            desc.EngineTag,
		commit:         commit,
		cpythonSHA:     cpythonSHA,
		meteredSHA:     meteredSHA,
		darwinLibSHA:   sha256Hex(darwinLib),
		linuxLibSHA:    sha256Hex(linuxLib),
	}
}

func TestSelectFetchStageVerifyEngineRelease(t *testing.T) {
	ctx := context.Background()
	fx := createEngineTestFixture(t)
	workDir := t.TempDir()
	manifestPath := filepath.Join(workDir, "bundled-manifest.yaml")

	// Reject wrong tag or wrong commit.
	if _, err := SelectEngineRelease(ctx, SelectEngineReleaseRequest{
		DescriptorSource: fx.descriptorPath,
		Tag:              "wrong-tag",
		ExpectedCommit:   fx.commit,
		ManifestPath:     manifestPath,
	}); err == nil {
		t.Fatal("expected error for mismatched tag")
	}
	if _, err := SelectEngineRelease(ctx, SelectEngineReleaseRequest{
		DescriptorSource: fx.descriptorPath,
		Tag:              fx.tag,
		ExpectedCommit:   strings.Repeat("a", 40),
		ManifestPath:     manifestPath,
	}); err == nil {
		t.Fatal("expected error for mismatched commit")
	}

	// Valid selection writes bundled-manifest.yaml.
	m, err := SelectEngineRelease(ctx, SelectEngineReleaseRequest{
		DescriptorSource: fx.descriptorPath,
		Tag:              fx.tag,
		ExpectedCommit:   fx.commit,
		ManifestPath:     manifestPath,
	})
	if err != nil {
		t.Fatalf("SelectEngineRelease failed: %v", err)
	}

	loaded, err := LoadBundledEngineManifestFile(manifestPath)
	if err != nil {
		t.Fatalf("LoadBundledEngineManifestFile failed: %v", err)
	}

	// Build identity round-trip for both platforms.
	for _, plat := range [][2]string{{"darwin", "arm64"}, {"linux", "amd64"}} {
		encoded, err := loaded.EncodedBuildIdentity(plat[0], plat[1])
		if err != nil {
			t.Fatalf("EncodedBuildIdentity(%s/%s): %v", plat[0], plat[1], err)
		}
		if _, err := ManifestWithEngineBuildIdentity(loaded, encoded); err != nil {
			t.Fatalf("ManifestWithEngineBuildIdentity(%s/%s): %v", plat[0], plat[1], err)
		}

		cacheDir := filepath.Join(workDir, "cache", plat[0]+"-"+plat[1])
		fetchedDir, err := FetchEngineArtifact(ctx, m, plat[0], plat[1], cacheDir, nil)
		if err != nil {
			t.Fatalf("FetchEngineArtifact(%s/%s): %v", plat[0], plat[1], err)
		}

		stageRoot := filepath.Join(workDir, "stage-"+plat[0]+"-"+plat[1])
		libPath, err := StageEngineArtifact(ctx, m, fetchedDir, stageRoot, plat[0], plat[1])
		if err != nil {
			t.Fatalf("StageEngineArtifact(%s/%s): %v", plat[0], plat[1], err)
		}
		if _, err := VerifyStagedEngineArtifact(m, stageRoot, plat[0], plat[1]); err != nil {
			t.Fatalf("VerifyStagedEngineArtifact(%s/%s): %v", plat[0], plat[1], err)
		}

		// Tampering the staged library must fail verification.
		if err := os.WriteFile(libPath, []byte("tampered"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyStagedEngineArtifact(m, stageRoot, plat[0], plat[1]); err == nil {
			t.Fatalf("expected VerifyStagedEngineArtifact to fail on tampered %s/%s library", plat[0], plat[1])
		}
	}
}

func TestVerifyNativeEngineHandshakeFailClosed(t *testing.T) {
	ctx := context.Background()
	fx := createEngineTestFixture(t)
	workDir := t.TempDir()
	manifestPath := filepath.Join(workDir, "bundled-manifest.yaml")

	m, err := SelectEngineRelease(ctx, SelectEngineReleaseRequest{
		DescriptorSource: fx.descriptorPath,
		Tag:              fx.tag,
		ExpectedCommit:   fx.commit,
		ManifestPath:     manifestPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	goos, goarch := "darwin", "arm64"
	cacheDir := filepath.Join(workDir, "cache")
	fetchedDir, err := FetchEngineArtifact(ctx, m, goos, goarch, cacheDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	stageRoot := filepath.Join(workDir, "stage")
	if _, err := StageEngineArtifact(ctx, m, fetchedDir, stageRoot, goos, goarch); err != nil {
		t.Fatal(err)
	}
	engineDir := StagedEngineDir(stageRoot)

	// Build a minimal verified paired image directory.
	imgDir := filepath.Join(workDir, "image")
	if err := os.MkdirAll(filepath.Join(imgDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(imgDir, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(imgDir, "packages"), 0o755); err != nil {
		t.Fatal(err)
	}
	wasmBytes := []byte("fake-wasm-for-handshake-test")
	wasmPath := filepath.Join(imgDir, "bin", "python.wasm")
	if err := os.WriteFile(wasmPath, wasmBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imgDir, "lib", "site.py"), []byte("# stdlib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imgDir, "packages", "pkg.py"), []byte("X = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wasmSHA := sha256Hex(wasmBytes)
	stdlibSHA, err := HashTree(filepath.Join(imgDir, "lib"))
	if err != nil {
		t.Fatal(err)
	}
	pkgSHA, err := HashTree(filepath.Join(imgDir, "packages"))
	if err != nil {
		t.Fatal(err)
	}

	// Align bundled manifest & staged artifact-manifest.json cpython digest to wasmSHA for clean baseline test.
	m.Engine.CPythonWasmSHA256 = wasmSHA
	if _, err := StageEngineArtifact(ctx, m, fetchedDir, stageRoot, goos, goarch); err != nil {
		t.Fatal(err)
	}

	imgManifest := &ImageManifest{
		SchemaVersion: ImageManifestSchemaVersion,
		ImageID:       "test-image-1",
		Interpreter: InterpreterSpec{
			Name:              "cpython",
			Version:           "3.12.0",
			WasmPath:          "bin/python.wasm",
			WasmSHA256:        wasmSHA,
			MeteredWasmSHA256: fx.meteredSHA,
			StdlibPath:        "lib",
			StdlibSHA256:      stdlibSHA,
		},
		PackagesRoot:          "packages",
		PackagesTreeSHA256:    pkgSHA,
		ImportInventorySHA256: ExpectedNativeEngineImportInventorySHA256,
	}

	goodID := &NativeEngineIdentity{
		ABIVersion:            ExpectedNativeEngineABIVersion,
		ImportCount:           ExpectedNativeEngineImportCount,
		EngineTag:             fx.tag,
		SourceCommit:          fx.commit,
		Platform:              "darwin-arm64",
		CPythonVersion:        "3.12.0",
		CPythonWasmSHA256:     wasmSHA,
		MeteredWasmSHA256:     fx.meteredSHA,
		WABTVersion:           "1.0.42",
		FuelAlgorithmVersion:  "pw-loop-func-i64/v1",
		ImportInventorySHA256: ExpectedNativeEngineImportInventorySHA256,
	}

	// 1. Valid pairing passes.
	if _, err := VerifyNativeEngineHandshake(HandshakeVerificationInput{
		BundledManifest:  m,
		EngineDir:        engineDir,
		ImageDir:         imgDir,
		ImageManifest:    imgManifest,
		ReportedIdentity: goodID,
		GOOS:             goos,
		GOARCH:           goarch,
	}); err != nil {
		t.Fatalf("expected valid handshake to succeed, got: %v", err)
	}

	// 2. Every mismatch fails closed with the exact expected stage.
	cases := []struct {
		name      string
		wantStage string
		mutate    func(id *NativeEngineIdentity, im *ImageManifest)
	}{
		{
			name:      "abi version skew",
			wantStage: "abi",
			mutate:    func(id *NativeEngineIdentity, _ *ImageManifest) { id.ABIVersion = 99 },
		},
		{
			name:      "platform skew",
			wantStage: "platform",
			mutate:    func(id *NativeEngineIdentity, _ *ImageManifest) { id.Platform = "linux-amd64" },
		},
		{
			name:      "cpython wasm skew",
			wantStage: "cpython_wasm",
			mutate:    func(id *NativeEngineIdentity, _ *ImageManifest) { id.CPythonWasmSHA256 = strings.Repeat("b", 64) },
		},
		{
			name:      "metered wasm skew",
			wantStage: "metered_wasm",
			mutate:    func(id *NativeEngineIdentity, _ *ImageManifest) { id.MeteredWasmSHA256 = strings.Repeat("c", 64) },
		},
		{
			name:      "import inventory skew",
			wantStage: "import_inventory",
			mutate:    func(id *NativeEngineIdentity, _ *ImageManifest) { id.ImportInventorySHA256 = strings.Repeat("d", 64) },
		},
		{
			name:      "image metered wasm skew",
			wantStage: "metered_wasm",
			mutate:    func(_ *NativeEngineIdentity, im *ImageManifest) { im.Interpreter.MeteredWasmSHA256 = strings.Repeat("e", 64) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idCopy := *goodID
			imCopy := *imgManifest
			tc.mutate(&idCopy, &imCopy)
			_, err := VerifyNativeEngineHandshake(HandshakeVerificationInput{
				BundledManifest:  m,
				EngineDir:        engineDir,
				ImageDir:         imgDir,
				ImageManifest:    &imCopy,
				ReportedIdentity: &idCopy,
				GOOS:             goos,
				GOARCH:           goarch,
			})
			var ve *NativeEngineVerificationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected NativeEngineVerificationError, got %v", err)
			}
			if ve.Stage != tc.wantStage {
				t.Fatalf("stage=%q want %q (err=%v)", ve.Stage, tc.wantStage, err)
			}
		})
	}
}

func TestVerifyImageBytecodeCheckedHash(t *testing.T) {
	imgDir := t.TempDir()
	for _, sub := range []string{"bin", "lib", "packages/__pycache__"} {
		if err := os.MkdirAll(filepath.Join(imgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	wasmBytes := []byte("wasm")
	if err := os.WriteFile(filepath.Join(imgDir, "bin", "python.wasm"), wasmBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imgDir, "lib", "site.py"), []byte("# ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imgDir, "packages", "mod.py"), []byte("VAL = 42\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Valid PEP 552 checked-hash header: magic cb0d0d0a + flags 0x03 (little-endian) + 8-byte hash + body.
	validPyc := []byte{
		0xcb, 0x0d, 0x0d, 0x0a,
		0x03, 0x00, 0x00, 0x00,
		0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88,
		0xe3, 0x00, 0x00, 0x00,
	}
	pycPath := filepath.Join(imgDir, "packages", "__pycache__", "mod.cpython-312.pyc")
	if err := os.WriteFile(pycPath, validPyc, 0o644); err != nil {
		t.Fatal(err)
	}

	stdlibSHA, _ := HashTree(filepath.Join(imgDir, "lib"))
	pkgSHA, _ := HashTree(filepath.Join(imgDir, "packages"))
	pycTreeSHA, _ := hashRelativeFiles(imgDir, []string{"packages/__pycache__/mod.cpython-312.pyc"})

	im := &ImageManifest{
		SchemaVersion: ImageManifestSchemaVersion,
		ImageID:       "bytecode-test",
		Interpreter: InterpreterSpec{
			Name:         "cpython",
			Version:      "3.12.0",
			WasmPath:     "bin/python.wasm",
			WasmSHA256:   sha256Hex(wasmBytes),
			StdlibPath:   "lib",
			StdlibSHA256: stdlibSHA,
		},
		PackagesRoot:       "packages",
		PackagesTreeSHA256: pkgSHA,
		Bytecode: &BytecodeSpec{
			InvalidationMode: "checked-hash",
			CPythonVersion:   "3.12",
			MagicHex:         CPython312BytecodeMagicHex,
			PycCount:         1,
			PycTreeSHA256:    pycTreeSHA,
		},
	}
	if err := VerifyImage(imgDir, im); err != nil {
		t.Fatalf("VerifyImage failed on valid checked-hash bytecode: %v", err)
	}

	// Timestamp-based .pyc (flags=0x00) must be rejected even if hashes are updated.
	timestampPyc := append([]byte(nil), validPyc...)
	timestampPyc[4] = 0x00
	if err := os.WriteFile(pycPath, timestampPyc, 0o644); err != nil {
		t.Fatal(err)
	}
	im.PackagesTreeSHA256, _ = HashTree(filepath.Join(imgDir, "packages"))
	im.Bytecode.PycTreeSHA256, _ = hashRelativeFiles(imgDir, []string{"packages/__pycache__/mod.cpython-312.pyc"})
	if err := VerifyImage(imgDir, im); err == nil {
		t.Fatal("expected VerifyImage to reject timestamp-based .pyc")
	}
}

func TestNewConfiguredRuntimeNoSilentFallback(t *testing.T) {
	_, err := NewConfiguredRuntime(context.Background(), RuntimeConfig{
		Backend:   BackendNativeAOT,
		EngineDir: filepath.Join(t.TempDir(), "nonexistent-engine"),
	})
	if err == nil {
		t.Fatalf("expected fail-closed error when BackendNativeAOT is requested on %s/%s without staged engine", runtime.GOOS, runtime.GOARCH)
	}
	var ve *NativeEngineVerificationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected NativeEngineVerificationError (no fallback), got %T: %v", err, err)
	}
}
