package pythonruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// legacyVerifyImageReference reproduces the pre-coalesced multi-pass VerifyImage
// semantics so unit tests can prove exact equivalence across valid and tampered images.
func legacyVerifyImageReference(dir string, manifest *ImageManifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	wasmPath, err := resolveInsideDir(absDir, manifest.Interpreter.WasmPath)
	if err != nil {
		return err
	}
	wasmDigest, err := HashFile(wasmPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(wasmDigest, manifest.Interpreter.WasmSHA256) {
		return fmt.Errorf("interpreter wasm sha256 mismatch: got %s, want %s", wasmDigest, manifest.Interpreter.WasmSHA256)
	}
	stdlibPath, err := resolveInsideDir(absDir, manifest.Interpreter.StdlibPath)
	if err != nil {
		return err
	}
	stdlibDigest, err := HashPath(stdlibPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(stdlibDigest, manifest.Interpreter.StdlibSHA256) {
		return fmt.Errorf("interpreter stdlib sha256 mismatch: got %s, want %s", stdlibDigest, manifest.Interpreter.StdlibSHA256)
	}
	if strings.TrimSpace(manifest.PackagesRoot) != "" {
		pkgRoot, err := resolveInsideDir(absDir, manifest.PackagesRoot)
		if err != nil {
			return err
		}
		pkgTree, err := HashTree(pkgRoot)
		if err != nil {
			return err
		}
		if !strings.EqualFold(pkgTree, manifest.PackagesTreeSHA256) {
			return fmt.Errorf("packages_root tree sha256 mismatch: got %s, want %s", pkgTree, manifest.PackagesTreeSHA256)
		}
		for _, dist := range manifest.Distributions {
			if strings.TrimSpace(dist.DistInfoPath) == "" || !isHexSHA256(dist.TreeSHA256) {
				continue
			}
			distInfoAbs, err := resolveInsideDir(absDir, dist.DistInfoPath)
			if err != nil {
				return err
			}
			recBytes, err := os.ReadFile(filepath.Join(distInfoAbs, "RECORD"))
			if err != nil {
				return err
			}
			var rels []string
			for _, line := range strings.Split(string(recBytes), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if rel := strings.SplitN(line, ",", 2)[0]; rel != "" {
					rels = append(rels, rel)
				}
			}
			got, err := hashRelativeFiles(pkgRoot, rels)
			if err != nil {
				return err
			}
			if !strings.EqualFold(got, dist.TreeSHA256) {
				return fmt.Errorf("distribution %s tree sha256 mismatch: got %s, want %s", dist.Name, got, dist.TreeSHA256)
			}
		}
	}
	if manifest.Bytecode != nil {
		magicBytes, _ := hex.DecodeString(CPython312BytecodeMagicHex)
		var pycRels []string
		if err := filepath.WalkDir(absDir, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".pyc") {
				return walkErr
			}
			rel, _ := filepath.Rel(absDir, p)
			relSlash := filepath.ToSlash(rel)
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if len(raw) < 16 {
				return fmt.Errorf("truncated bytecode header in %s", relSlash)
			}
			if !bytes.Equal(raw[:4], magicBytes) {
				return fmt.Errorf("bytecode magic mismatch in %s", relSlash)
			}
			flags := uint32(raw[4]) | uint32(raw[5])<<8 | uint32(raw[6])<<16 | uint32(raw[7])<<24
			if (flags & 0x3) != 0x3 {
				return fmt.Errorf("bytecode %s is not PEP 552 checked-hash", relSlash)
			}
			pycRels = append(pycRels, relSlash)
			return nil
		}); err != nil {
			return err
		}
		if len(pycRels) != manifest.Bytecode.PycCount {
			return fmt.Errorf("image bytecode count mismatch: got %d want %d", len(pycRels), manifest.Bytecode.PycCount)
		}
		gotPyc, err := hashRelativeFiles(absDir, pycRels)
		if err != nil {
			return err
		}
		if !strings.EqualFold(gotPyc, manifest.Bytecode.PycTreeSHA256) {
			return fmt.Errorf("image bytecode pyc_tree_sha256 mismatch: got %s want %s", gotPyc, manifest.Bytecode.PycTreeSHA256)
		}
	}
	var native []string
	_ = filepath.WalkDir(absDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		if isHostNativeBinaryName(d.Name()) {
			rel, _ := filepath.Rel(absDir, p)
			native = append(native, filepath.ToSlash(rel))
		}
		return nil
	})
	if len(native) > 0 {
		sort.Strings(native)
		return fmt.Errorf("host native binaries forbidden in WASI image: %s", strings.Join(native, ", "))
	}
	return nil
}

func makeCheckedHashPyc(payload []byte) []byte {
	magic, _ := hex.DecodeString(CPython312BytecodeMagicHex)
	out := make([]byte, 0, 16+len(payload))
	out = append(out, magic...)
	out = append(out, 0x03, 0x00, 0x00, 0x00) // PEP 552 checked-hash flags (bit0|bit1)
	out = append(out, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88)
	out = append(out, payload...)
	return out
}

func buildSyntheticVerifiedImage(t *testing.T) (string, *ImageManifest, []byte) {
	t.Helper()
	dir := t.TempDir()
	wasmBytes := []byte("\x00asm\x01\x00\x00\x00synthetic-wasm-bytes")
	if err := os.WriteFile(filepath.Join(dir, "python.wasm"), wasmBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	stdlibDir := filepath.Join(dir, "stdlib")
	if err := os.MkdirAll(filepath.Join(stdlibDir, "__pycache__"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stdlibDir, "os.py"), []byte("NAME = 'wasi'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stdlibDir, "__pycache__", "os.cpython-312.pyc"), makeCheckedHashPyc([]byte("stdlib-os")), 0o644); err != nil {
		t.Fatal(err)
	}

	pkgRoot := filepath.Join(dir, "packages")
	if err := os.MkdirAll(filepath.Join(pkgRoot, "alpha", "__pycache__"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pkgRoot, "alpha-1.0.0.dist-info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgRoot, "alpha", "__init__.py"), []byte("VAL = 42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgRoot, "alpha", "data.txt"), []byte("resource\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgRoot, "alpha", "__pycache__", "__init__.cpython-312.pyc"), makeCheckedHashPyc([]byte("alpha-init")), 0o644); err != nil {
		t.Fatal(err)
	}
	recordContent := strings.Join([]string{
		"alpha/__init__.py,sha256=abc,9",
		"alpha/data.txt,sha256=def,9",
		"alpha-1.0.0.dist-info/RECORD,,",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(pkgRoot, "alpha-1.0.0.dist-info", "RECORD"), []byte(recordContent), 0o644); err != nil {
		t.Fatal(err)
	}

	wasmSum := sha256.Sum256(wasmBytes)
	stdlibSHA, err := HashTree(stdlibDir)
	if err != nil {
		t.Fatal(err)
	}
	pkgSHA, err := HashTree(pkgRoot)
	if err != nil {
		t.Fatal(err)
	}
	distSHA, err := hashRelativeFiles(pkgRoot, []string{"alpha/__init__.py", "alpha/data.txt", "alpha-1.0.0.dist-info/RECORD"})
	if err != nil {
		t.Fatal(err)
	}
	pycTreeSHA, err := hashRelativeFiles(dir, []string{
		"stdlib/__pycache__/os.cpython-312.pyc",
		"packages/alpha/__pycache__/__init__.cpython-312.pyc",
	})
	if err != nil {
		t.Fatal(err)
	}

	im := &ImageManifest{
		SchemaVersion: ImageManifestSchemaVersion,
		ImageID:       "pp-cpython-3.12.0-wasi-synthetic",
		Target:        "wasm32-wasi",
		Interpreter: InterpreterSpec{
			Name:         "cpython",
			Version:      "3.12.0",
			WasmPath:     "python.wasm",
			WasmSHA256:   hex.EncodeToString(wasmSum[:]),
			StdlibPath:   "stdlib",
			StdlibSHA256: stdlibSHA,
		},
		PackagesRoot:       "packages",
		PackagesTreeSHA256: pkgSHA,
		Bytecode: &BytecodeSpec{
			InvalidationMode: "checked-hash",
			CPythonVersion:   "3.12.0",
			MagicHex:         CPython312BytecodeMagicHex,
			PycCount:         2,
			PycTreeSHA256:    pycTreeSHA,
		},
		Distributions: []DistributionSpec{
			{
				Name:           "alpha",
				NormalizedName: "alpha",
				Version:        "1.0.0",
				Public:         true,
				Imports:        []string{"alpha"},
				DistInfoPath:   "packages/alpha-1.0.0.dist-info",
				TreeSHA256:     distSHA,
			},
		},
	}
	return dir, im, wasmBytes
}

func TestVerifyImageCoalescedEquivalenceAndTamperControls(t *testing.T) {
	t.Run("clean image matches legacy reference and returns verified wasm bytes", func(t *testing.T) {
		dir, im, wasmBytes := buildSyntheticVerifiedImage(t)
		if err := legacyVerifyImageReference(dir, im); err != nil {
			t.Fatalf("legacy reference failed on clean image: %v", err)
		}
		v, err := VerifyImageResult(dir, im)
		if err != nil {
			t.Fatalf("VerifyImageResult failed on clean image: %v", err)
		}
		if !bytes.Equal(v.WasmBytes(), wasmBytes) {
			t.Fatalf("VerifiedImage.WasmBytes mismatch")
		}
		if !v.matches(dir, im) {
			t.Fatalf("VerifiedImage.matches returned false for identical (dir, manifest)")
		}
	})

	tamperCases := []struct {
		name        string
		mutate      func(t *testing.T, dir string, im *ImageManifest)
		wantSubstr  string
	}{
		{
			name: "mutate .py file",
			mutate: func(t *testing.T, dir string, _ *ImageManifest) {
				if err := os.WriteFile(filepath.Join(dir, "packages", "alpha", "__init__.py"), []byte("VAL = 999\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantSubstr: "packages_root tree sha256 mismatch",
		},
		{
			name: "mutate RECORD-listed file with recomputed packages tree",
			mutate: func(t *testing.T, dir string, im *ImageManifest) {
				if err := os.WriteFile(filepath.Join(dir, "packages", "alpha", "data.txt"), []byte("tampered\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				im.PackagesTreeSHA256, _ = HashTree(filepath.Join(dir, "packages"))
			},
			wantSubstr: "distribution alpha tree sha256 mismatch",
		},
		{
			name: "mutate .pyc header to timestamp mode",
			mutate: func(t *testing.T, dir string, im *ImageManifest) {
				pycPath := filepath.Join(dir, "packages", "alpha", "__pycache__", "__init__.cpython-312.pyc")
				bad := makeCheckedHashPyc([]byte("alpha-init"))
				bad[4] = 0x00 // timestamp invalidation mode
				if err := os.WriteFile(pycPath, bad, 0o644); err != nil {
					t.Fatal(err)
				}
				im.PackagesTreeSHA256, _ = HashTree(filepath.Join(dir, "packages"))
				im.Bytecode.PycTreeSHA256, _ = hashRelativeFiles(dir, []string{
					"stdlib/__pycache__/os.cpython-312.pyc",
					"packages/alpha/__pycache__/__init__.cpython-312.pyc",
				})
			},
			wantSubstr: "not PEP 552 checked-hash",
		},
		{
			name: "mutate .pyc payload byte",
			mutate: func(t *testing.T, dir string, im *ImageManifest) {
				pycPath := filepath.Join(dir, "packages", "alpha", "__pycache__", "__init__.cpython-312.pyc")
				if err := os.WriteFile(pycPath, makeCheckedHashPyc([]byte("tampered-bytecode")), 0o644); err != nil {
					t.Fatal(err)
				}
				im.PackagesTreeSHA256, _ = HashTree(filepath.Join(dir, "packages"))
			},
			wantSubstr: "pyc_tree_sha256 mismatch",
		},
		{
			name: "mutate python.wasm",
			mutate: func(t *testing.T, dir string, _ *ImageManifest) {
				if err := os.WriteFile(filepath.Join(dir, "python.wasm"), []byte("corrupted-wasm"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantSubstr: "wasm sha256 mismatch",
		},
		{
			name: "plant .so native binary",
			mutate: func(t *testing.T, dir string, _ *ImageManifest) {
				if err := os.WriteFile(filepath.Join(dir, "injected.so"), []byte("ELF"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantSubstr: "host native binaries forbidden",
		},
		{
			name: "plant .dylib native binary",
			mutate: func(t *testing.T, dir string, _ *ImageManifest) {
				if err := os.WriteFile(filepath.Join(dir, "injected.dylib"), []byte("MACHO"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantSubstr: "host native binaries forbidden",
		},
	}

	for _, tc := range tamperCases {
		t.Run(tc.name, func(t *testing.T) {
			dir, im, _ := buildSyntheticVerifiedImage(t)
			tc.mutate(t, dir, im)

			legacyErr := legacyVerifyImageReference(dir, im)
			gotErr := VerifyImage(dir, im)
			if legacyErr == nil || gotErr == nil {
				t.Fatalf("expected both legacy (%v) and coalesced (%v) to fail", legacyErr, gotErr)
			}
			if !strings.Contains(gotErr.Error(), tc.wantSubstr) {
				t.Fatalf("coalesced error %q missing expected %q (legacy=%q)", gotErr.Error(), tc.wantSubstr, legacyErr.Error())
			}
			if !strings.Contains(legacyErr.Error(), tc.wantSubstr) {
				t.Fatalf("legacy error %q missing expected %q", legacyErr.Error(), tc.wantSubstr)
			}
		})
	}
}

func TestVerifyNativeEngineHandshakeVerifiedImageControls(t *testing.T) {
	dirA, imA, _ := buildSyntheticVerifiedImage(t)
	dirB, imB, _ := buildSyntheticVerifiedImage(t)

	vA, err := VerifyImageResult(dirA, imA)
	if err != nil {
		t.Fatalf("VerifyImageResult(dirA): %v", err)
	}

	// Forged (unverified) struct must never match.
	forged := &VerifiedImage{imageDir: dirA, manifest: imA}
	if forged.matches(dirA, imA) {
		t.Fatal("forged VerifiedImage with verified=false must not match")
	}

	// Valid token for dirA must not match dirB.
	if vA.matches(dirB, imB) {
		t.Fatal("VerifiedImage for dirA must not match dirB")
	}

	// Mutating manifest after VerifyImageResult must invalidate the token.
	imMutated := *imA
	imMutated.ImageID = "different-id"
	if vA.matches(dirA, &imMutated) {
		t.Fatal("VerifiedImage must not match a mutated ImageManifest")
	}

	// Standalone VerifyNativeEngineHandshake alternate-entry control:
	// without a matching VerifiedImage token, tampering MUST fail closed at stage "image".
	ctx := context.Background()
	fx := createEngineTestFixture(t)
	workDir := t.TempDir()
	m, err := SelectEngineRelease(ctx, SelectEngineReleaseRequest{
		DescriptorSource: fx.descriptorPath,
		Tag:              fx.tag,
		ExpectedCommit:   fx.commit,
		ManifestPath:     filepath.Join(workDir, "bundled-manifest.yaml"),
	})
	if err != nil {
		t.Fatal(err)
	}
	goos, goarch := "darwin", "arm64"
	fetchedDir, err := FetchEngineArtifact(ctx, m, goos, goarch, filepath.Join(workDir, "cache"), nil)
	if err != nil {
		t.Fatal(err)
	}
	m.Engine.CPythonWasmSHA256 = imA.Interpreter.WasmSHA256
	imA.Interpreter.MeteredWasmSHA256 = fx.meteredSHA
	imA.ImportInventorySHA256 = ExpectedNativeEngineImportInventorySHA256
	stageRoot := filepath.Join(workDir, "stage")
	if _, err := StageEngineArtifact(ctx, m, fetchedDir, stageRoot, goos, goarch); err != nil {
		t.Fatal(err)
	}
	engineDir := StagedEngineDir(stageRoot)
	goodID := &NativeEngineIdentity{
		ABIVersion:            ExpectedNativeEngineABIVersion,
		ImportCount:           ExpectedNativeEngineImportCount,
		EngineTag:             fx.tag,
		SourceCommit:          fx.commit,
		Platform:              "darwin-arm64",
		CPythonVersion:        "3.12.0",
		CPythonWasmSHA256:     imA.Interpreter.WasmSHA256,
		MeteredWasmSHA256:     fx.meteredSHA,
		WABTVersion:           "1.0.42",
		FuelAlgorithmVersion:  "pw-loop-func-i64/v1",
		ImportInventorySHA256: ExpectedNativeEngineImportInventorySHA256,
	}

	// Clean standalone call succeeds.
	if _, err := VerifyNativeEngineHandshake(HandshakeVerificationInput{
		BundledManifest:  m,
		EngineDir:        engineDir,
		ImageDir:         dirA,
		ImageManifest:    imA,
		ReportedIdentity: goodID,
		GOOS:             goos,
		GOARCH:           goarch,
	}); err != nil {
		t.Fatalf("clean standalone VerifyNativeEngineHandshake failed: %v", err)
	}

	// Tamper dirA on disk, then verify standalone calls (nil, forged, and wrong-dir token) all fail closed.
	if err := os.WriteFile(filepath.Join(dirA, "packages", "alpha", "__init__.py"), []byte("TAMPERED = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	vB, err := VerifyImageResult(dirB, imB)
	if err != nil {
		t.Fatalf("VerifyImageResult(dirB): %v", err)
	}
	for _, tc := range []struct {
		name  string
		token *VerifiedImage
	}{
		{name: "standalone nil VerifiedImage", token: nil},
		{name: "forged unverified VerifiedImage", token: &VerifiedImage{imageDir: dirA, manifest: imA}},
		{name: "valid token from different imageDir", token: vB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := VerifyNativeEngineHandshake(HandshakeVerificationInput{
				BundledManifest:  m,
				EngineDir:        engineDir,
				ImageDir:         dirA,
				ImageManifest:    imA,
				VerifiedImage:    tc.token,
				ReportedIdentity: goodID,
				GOOS:             goos,
				GOARCH:           goarch,
			})
			var ve *NativeEngineVerificationError
			if !errors.As(err, &ve) || ve.Stage != "image" {
				t.Fatalf("expected fail-closed stage=image error, got %v", err)
			}
		})
	}
}
