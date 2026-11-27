package pythonruntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const maxEngineArtifactBytes = 512 << 20

// SelectEngineReleaseRequest configures selecting an authenticated PY07 release
// into bundled-manifest.yaml.
type SelectEngineReleaseRequest struct {
	// DescriptorSource is a local file path or HTTPS URL to PY07's release descriptor JSON.
	DescriptorSource string
	// ArtifactBaseURL optionally overrides the base URL/directory prepended to
	// each platform artifact's archive_name when the descriptor omits per-artifact url.
	ArtifactBaseURL string
	Tag             string
	ExpectedCommit  string
	ManifestPath    string
	HTTPClient      *http.Client
}

// SelectEngineRelease fetches/reads PY07's release descriptor, validates its
// tag, expected commit, ABI version, ABI header digest, CPython/metered/import
// digests, zero JIT entitlements, and platform artifacts, and writes the pinned
// bundled-manifest.yaml.
func SelectEngineRelease(ctx context.Context, req SelectEngineReleaseRequest) (*BundledEngineManifest, error) {
	if strings.TrimSpace(req.DescriptorSource) == "" {
		return nil, errors.New("select engine release: descriptor source is required")
	}
	if strings.TrimSpace(req.Tag) == "" {
		return nil, errors.New("select engine release: --tag is required")
	}
	expectedCommit := strings.ToLower(strings.TrimSpace(req.ExpectedCommit))
	if !isLowerHexLen(expectedCommit, 40) {
		return nil, fmt.Errorf("select engine release: --expected-commit must be a 40-char lowercase git SHA (got %q)", req.ExpectedCommit)
	}

	raw, err := readSourceBytes(ctx, req.DescriptorSource, req.HTTPClient)
	if err != nil {
		return nil, fmt.Errorf("read release descriptor: %w", err)
	}
	sum := sha256.Sum256(raw)
	descriptorSHA := hex.EncodeToString(sum[:])

	var desc EngineReleaseDescriptor
	if err := json.Unmarshal(raw, &desc); err != nil {
		return nil, fmt.Errorf("decode release descriptor: %w", err)
	}
	if desc.SchemaVersion != EngineReleaseDescriptorSchemaVersion {
		return nil, fmt.Errorf("unsupported release descriptor schema_version %q (want %q)", desc.SchemaVersion, EngineReleaseDescriptorSchemaVersion)
	}
	if desc.EngineTag != req.Tag {
		return nil, fmt.Errorf("release descriptor engine_tag mismatch: got %q want %q", desc.EngineTag, req.Tag)
	}
	if !strings.EqualFold(desc.SourceCommit, expectedCommit) {
		return nil, fmt.Errorf("release descriptor source_commit mismatch: got %q want %q", desc.SourceCommit, expectedCommit)
	}
	if desc.ABIVersion != ExpectedNativeEngineABIVersion {
		return nil, fmt.Errorf("release descriptor abi_version %d does not match expected %d", desc.ABIVersion, ExpectedNativeEngineABIVersion)
	}
	if !strings.EqualFold(desc.ABIHeaderSHA256, ExpectedNativeEngineABIHeaderSHA256) {
		return nil, fmt.Errorf("release descriptor abi_header_sha256 mismatch: got %s want %s", desc.ABIHeaderSHA256, ExpectedNativeEngineABIHeaderSHA256)
	}
	if desc.ImportInventory.Count != ExpectedNativeEngineImportCount {
		return nil, fmt.Errorf("release descriptor import_inventory.count %d does not match expected %d", desc.ImportInventory.Count, ExpectedNativeEngineImportCount)
	}
	if !strings.EqualFold(desc.ImportInventory.SHA256, ExpectedNativeEngineImportInventorySHA256) {
		return nil, fmt.Errorf("release descriptor import_inventory.sha256 mismatch: got %s want %s", desc.ImportInventory.SHA256, ExpectedNativeEngineImportInventorySHA256)
	}

	upstreamVer := strings.TrimSpace(desc.CPython.Version)
	if upstreamVer == "" {
		upstreamVer = "3.12.0"
	}
	rev := desc.Revision
	if rev == 0 {
		rev = 1
	}
	version := fmt.Sprintf("%s+paintedwolf.%d", upstreamVer, rev)
	license := strings.TrimSpace(desc.License)
	if license == "" {
		license = "PSF-2.0 AND Apache-2.0"
	}

	// Determine default base location for resolving archive_name -> url/path.
	baseDir := ""
	baseURL := strings.TrimRight(strings.TrimSpace(req.ArtifactBaseURL), "/")
	if baseURL == "" {
		if isHTTPSURL(req.DescriptorSource) {
			if idx := strings.LastIndex(req.DescriptorSource, "/"); idx > len("https://") {
				baseURL = req.DescriptorSource[:idx]
			}
		} else if abs, absErr := filepath.Abs(req.DescriptorSource); absErr == nil {
			baseDir = filepath.Dir(abs)
		}
	}

	keys := make([]string, 0, len(desc.Artifacts))
	for k := range desc.Artifacts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	arts := make([]EngineReleaseArtifact, 0, len(keys))
	for _, k := range keys {
		da := desc.Artifacts[k]
		if len(da.JITEntitlements) > 0 {
			return nil, fmt.Errorf("release descriptor artifact %s declares forbidden jit_entitlements %v", k, da.JITEntitlements)
		}
		goos, goarch, err := EnginePlatformForTarget(da.Platform)
		if err != nil {
			goos, goarch, err = EnginePlatformForTarget(k)
			if err != nil {
				return nil, fmt.Errorf("release descriptor artifact %q: %w", k, err)
			}
		}
		if goos == "darwin" && !da.CodesignHardened {
			return nil, fmt.Errorf("release descriptor artifact %s must be hardened-runtime signed on darwin", k)
		}
		libName := strings.TrimSpace(da.LibraryName)
		if libName == "" {
			libName = DefaultLibraryName(goos)
		}
		artURL := strings.TrimSpace(da.URL)
		if artURL == "" {
			targetName := strings.TrimSpace(da.ArchiveName)
			if targetName == "" {
				targetName = libName
			}
			switch {
			case baseURL != "":
				artURL = baseURL + "/" + targetName
			case baseDir != "":
				artURL = filepath.Clean(filepath.Join(baseDir, targetName))
			default:
				artURL = targetName
			}
		} else if baseDir != "" && !isHTTPSURL(artURL) && !filepath.IsAbs(artURL) {
			artURL = filepath.Clean(filepath.Join(baseDir, artURL))
		}
		arts = append(arts, EngineReleaseArtifact{
			GOOS:             goos,
			GOARCH:           goarch,
			LibraryName:      libName,
			ArchiveName:      strings.TrimSpace(da.ArchiveName),
			URL:              artURL,
			SHA256:           strings.ToLower(strings.TrimSpace(da.LibrarySHA256)),
			Bytes:            da.LibraryBytes,
			ArchiveSHA256:    strings.ToLower(strings.TrimSpace(da.ArchiveSHA256)),
			ArchiveBytes:     da.ArchiveBytes,
			CodesignHardened: da.CodesignHardened,
		})
	}

	manifest := &BundledEngineManifest{
		Engine: EngineReleaseSpec{
			Version:               version,
			Origin:                "downstream",
			UpstreamVersion:       upstreamVer,
			BaseRevision:          expectedCommit,
			Revision:              rev,
			ABIVersion:            desc.ABIVersion,
			ABIHeaderSHA256:       strings.ToLower(strings.TrimSpace(desc.ABIHeaderSHA256)),
			CPythonWasmSHA256:     strings.ToLower(strings.TrimSpace(desc.CPython.WasmSHA256)),
			MeteredWasmSHA256:     strings.ToLower(strings.TrimSpace(desc.Metering.MeteredWasmSHA256)),
			ImportInventoryCount:  desc.ImportInventory.Count,
			ImportInventorySHA256: strings.ToLower(strings.TrimSpace(desc.ImportInventory.SHA256)),
			License:               license,
			Artifacts:             arts,
		},
		Selection: &EngineSelectionReference{
			Tag:              desc.EngineTag,
			ProducerCommit:   strings.ToLower(strings.TrimSpace(desc.SourceCommit)),
			DescriptorSHA256: descriptorSHA,
			EvidenceSHA256:   strings.ToLower(strings.TrimSpace(desc.EvidenceSHA256)),
		},
	}
	if err := ValidateBundledEngineManifest(manifest); err != nil {
		return nil, fmt.Errorf("validate selected manifest: %w", err)
	}
	if strings.TrimSpace(req.ManifestPath) != "" {
		out, err := yaml.Marshal(manifest)
		if err != nil {
			return nil, fmt.Errorf("encode bundled manifest yaml: %w", err)
		}
		header := "# Bundled Portable Python native AOT engine release manifest.\n# Selected by scripts/select-portable-python-engine.sh.\n"
		if err := os.MkdirAll(filepath.Dir(req.ManifestPath), 0o755); err != nil {
			return nil, fmt.Errorf("create manifest parent dir: %w", err)
		}
		if err := atomicWriteFile(req.ManifestPath, append([]byte(header), out...), 0o644); err != nil {
			return nil, fmt.Errorf("write bundled manifest %s: %w", req.ManifestPath, err)
		}
	}
	return manifest, nil
}

// FetchEngineArtifact downloads or copies the pinned platform library into
// outDir, verifies its archive and library SHA-256 and byte length, and writes
// artifact-manifest.json.
func FetchEngineArtifact(ctx context.Context, m *BundledEngineManifest, goos, goarch, outDir string, client *http.Client) (string, error) {
	art, err := m.ArtifactForPlatform(goos, goarch)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("create fetch dir %s: %w", outDir, err)
	}

	destLib := filepath.Join(outDir, art.LibraryName)
	destManifest := filepath.Join(outDir, "artifact-manifest.json")

	// Reuse verified cached artifact if already present and valid.
	if err := verifyEngineFile(destLib, art.SHA256, art.Bytes); err == nil {
		if _, merr := VerifyEngineArtifactManifestFile(destManifest, m, art); merr == nil {
			return outDir, nil
		}
	}

	raw, err := readSourceBytes(ctx, art.URL, client)
	if err != nil {
		return "", fmt.Errorf("fetch engine artifact from %s: %w", art.URL, err)
	}

	var libBytes []byte
	if strings.HasSuffix(strings.ToLower(art.URL), ".tar.gz") || strings.HasSuffix(strings.ToLower(art.URL), ".tgz") {
		if art.ArchiveSHA256 != "" && !isAllZerosHex(art.ArchiveSHA256) {
			got := sha256Hex(raw)
			if !strings.EqualFold(got, art.ArchiveSHA256) {
				return "", fmt.Errorf("engine archive sha256 mismatch: got %s want %s", got, art.ArchiveSHA256)
			}
		}
		if art.ArchiveBytes > 0 && int64(len(raw)) != art.ArchiveBytes {
			return "", fmt.Errorf("engine archive size mismatch: got %d want %d", len(raw), art.ArchiveBytes)
		}
		extracted, err := extractLibraryFromTarGz(raw, art.LibraryName)
		if err != nil {
			return "", err
		}
		libBytes = extracted
	} else {
		libBytes = raw
	}

	if art.Bytes > 0 && int64(len(libBytes)) != art.Bytes {
		return "", fmt.Errorf("engine library %s size mismatch: got %d want %d", art.LibraryName, len(libBytes), art.Bytes)
	}
	gotSHA := sha256Hex(libBytes)
	if !strings.EqualFold(gotSHA, art.SHA256) {
		return "", fmt.Errorf("engine library %s sha256 mismatch: got %s want %s", art.LibraryName, gotSHA, art.SHA256)
	}

	if err := atomicWriteFile(destLib, libBytes, 0o755); err != nil {
		return "", fmt.Errorf("write engine library: %w", err)
	}
	am := buildEngineArtifactManifest(m, art, int64(len(libBytes)))
	amBytes, err := json.MarshalIndent(am, "", "  ")
	if err != nil {
		return "", err
	}
	amBytes = append(amBytes, '\n')
	if err := atomicWriteFile(destManifest, amBytes, 0o644); err != nil {
		return "", fmt.Errorf("write artifact-manifest.json: %w", err)
	}
	return outDir, nil
}

// StageEngineArtifact installs the verified shared library and
// artifact-manifest.json from srcDir into <stageRoot>/portable-python/engine.
func StageEngineArtifact(ctx context.Context, m *BundledEngineManifest, srcDir, stageRoot, goos, goarch string) (string, error) {
	art, err := m.ArtifactForPlatform(goos, goarch)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	srcLib := filepath.Join(srcDir, art.LibraryName)
	if err := verifyEngineFile(srcLib, art.SHA256, art.Bytes); err != nil {
		return "", fmt.Errorf("verify source engine artifact: %w", err)
	}

	destDir := StagedEngineDir(stageRoot)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("create staged engine dir %s: %w", destDir, err)
	}
	// #nosec G304 -- verified source artifact path.
	libBytes, err := os.ReadFile(srcLib)
	if err != nil {
		return "", err
	}
	destLib := filepath.Join(destDir, art.LibraryName)
	if err := atomicWriteFile(destLib, libBytes, 0o755); err != nil {
		return "", fmt.Errorf("stage engine library: %w", err)
	}

	am := buildEngineArtifactManifest(m, art, int64(len(libBytes)))
	amBytes, err := json.MarshalIndent(am, "", "  ")
	if err != nil {
		return "", err
	}
	amBytes = append(amBytes, '\n')
	destManifest := filepath.Join(destDir, "artifact-manifest.json")
	if err := atomicWriteFile(destManifest, amBytes, 0o644); err != nil {
		return "", fmt.Errorf("stage artifact-manifest.json: %w", err)
	}

	return VerifyStagedEngineArtifact(m, stageRoot, goos, goarch)
}

// VerifyStagedEngineArtifact checks that <stageRoot>/portable-python/engine
// contains a regular, non-symlink shared library and artifact-manifest.json
// whose SHA-256, size, ABI, and digests exactly match m.
func VerifyStagedEngineArtifact(m *BundledEngineManifest, stageRoot, goos, goarch string) (string, error) {
	art, err := m.ArtifactForPlatform(goos, goarch)
	if err != nil {
		return "", err
	}
	dir := StagedEngineDir(stageRoot)
	return VerifyEngineDir(m, dir, goos, goarch, art)
}

// VerifyEngineDir verifies an engine directory directly against m and art.
func VerifyEngineDir(m *BundledEngineManifest, engineDir, goos, goarch string, art *EngineReleaseArtifact) (string, error) {
	if art == nil {
		var err error
		art, err = m.ArtifactForPlatform(goos, goarch)
		if err != nil {
			return "", err
		}
	}
	libPath := filepath.Join(engineDir, art.LibraryName)
	if err := verifyEngineFile(libPath, art.SHA256, art.Bytes); err != nil {
		return "", fmt.Errorf("verify staged native python engine library: %w", err)
	}
	manifestPath := filepath.Join(engineDir, "artifact-manifest.json")
	if _, err := VerifyEngineArtifactManifestFile(manifestPath, m, art); err != nil {
		return "", fmt.Errorf("verify staged native python engine manifest: %w", err)
	}
	return libPath, nil
}

// VerifyEngineArtifactManifestFile validates artifact-manifest.json against
// the bundled manifest and platform artifact pin.
func VerifyEngineArtifactManifestFile(path string, m *BundledEngineManifest, art *EngineReleaseArtifact) (*EngineArtifactManifest, error) {
	am, err := LoadEngineArtifactManifest(path)
	if err != nil {
		return nil, err
	}
	wantPlatform := art.GOOS + "-" + art.GOARCH
	if am.GOOS != art.GOOS || am.GOARCH != art.GOARCH || am.Platform != wantPlatform {
		return nil, fmt.Errorf("artifact manifest platform mismatch: got %s/%s (%s) want %s", am.GOOS, am.GOARCH, am.Platform, wantPlatform)
	}
	if am.ABIVersion != m.Engine.ABIVersion {
		return nil, fmt.Errorf("artifact manifest abi_version mismatch: got %d want %d", am.ABIVersion, m.Engine.ABIVersion)
	}
	if !strings.EqualFold(am.ABIHeaderSHA256, m.Engine.ABIHeaderSHA256) {
		return nil, fmt.Errorf("artifact manifest abi_header_sha256 mismatch: got %s want %s", am.ABIHeaderSHA256, m.Engine.ABIHeaderSHA256)
	}
	if am.LibraryName != art.LibraryName {
		return nil, fmt.Errorf("artifact manifest library_name mismatch: got %q want %q", am.LibraryName, art.LibraryName)
	}
	if !strings.EqualFold(am.LibrarySHA256, art.SHA256) {
		return nil, fmt.Errorf("artifact manifest library_sha256 mismatch: got %s want %s", am.LibrarySHA256, art.SHA256)
	}
	if art.Bytes > 0 && am.LibraryBytes != art.Bytes {
		return nil, fmt.Errorf("artifact manifest library_bytes mismatch: got %d want %d", am.LibraryBytes, art.Bytes)
	}
	if !strings.EqualFold(am.CPythonWasmSHA256, m.Engine.CPythonWasmSHA256) {
		return nil, fmt.Errorf("artifact manifest cpython_wasm_sha256 mismatch: got %s want %s", am.CPythonWasmSHA256, m.Engine.CPythonWasmSHA256)
	}
	if !strings.EqualFold(am.MeteredWasmSHA256, m.Engine.MeteredWasmSHA256) {
		return nil, fmt.Errorf("artifact manifest metered_wasm_sha256 mismatch: got %s want %s", am.MeteredWasmSHA256, m.Engine.MeteredWasmSHA256)
	}
	if am.ImportInventoryCount != m.Engine.ImportInventoryCount {
		return nil, fmt.Errorf("artifact manifest import_inventory_count mismatch: got %d want %d", am.ImportInventoryCount, m.Engine.ImportInventoryCount)
	}
	if !strings.EqualFold(am.ImportInventorySHA256, m.Engine.ImportInventorySHA256) {
		return nil, fmt.Errorf("artifact manifest import_inventory_sha256 mismatch: got %s want %s", am.ImportInventorySHA256, m.Engine.ImportInventorySHA256)
	}
	return am, nil
}

func buildEngineArtifactManifest(m *BundledEngineManifest, art *EngineReleaseArtifact, actualBytes int64) EngineArtifactManifest {
	libBytes := art.Bytes
	if libBytes <= 0 {
		libBytes = actualBytes
	}
	am := EngineArtifactManifest{
		SchemaVersion:         EngineArtifactManifestSchemaVersion,
		Version:               m.Engine.Version,
		GOOS:                  art.GOOS,
		GOARCH:                art.GOARCH,
		Platform:              art.GOOS + "-" + art.GOARCH,
		ABIVersion:            m.Engine.ABIVersion,
		ABIHeaderSHA256:       strings.ToLower(strings.TrimSpace(m.Engine.ABIHeaderSHA256)),
		LibraryName:           art.LibraryName,
		LibrarySHA256:         strings.ToLower(strings.TrimSpace(art.SHA256)),
		LibraryBytes:          libBytes,
		CPythonWasmSHA256:     strings.ToLower(strings.TrimSpace(m.Engine.CPythonWasmSHA256)),
		MeteredWasmSHA256:     strings.ToLower(strings.TrimSpace(m.Engine.MeteredWasmSHA256)),
		ImportInventoryCount:  m.Engine.ImportInventoryCount,
		ImportInventorySHA256: strings.ToLower(strings.TrimSpace(m.Engine.ImportInventorySHA256)),
	}
	if m.Selection != nil {
		am.EngineTag = m.Selection.Tag
		am.SourceCommit = m.Selection.ProducerCommit
	}
	return am
}

func verifyEngineFile(path, expectedSHA string, expectedBytes int64) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("engine artifact must be a regular non-symlink file: %s", path)
	}
	if expectedBytes > 0 && info.Size() != expectedBytes {
		return fmt.Errorf("engine artifact %s size mismatch: got %d want %d", path, info.Size(), expectedBytes)
	}
	got, err := HashFile(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, expectedSHA) {
		return fmt.Errorf("engine artifact %s sha256 mismatch: got %s want %s", path, got, expectedSHA)
	}
	return nil
}

func readSourceBytes(ctx context.Context, source string, client *http.Client) ([]byte, error) {
	if isHTTPSURL(source) {
		if client == nil {
			client = &http.Client{Timeout: 60 * time.Second}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, source)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxEngineArtifactBytes+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > maxEngineArtifactBytes {
			return nil, fmt.Errorf("artifact from %s exceeds size limit (%d bytes)", source, maxEngineArtifactBytes)
		}
		return data, nil
	}
	if strings.HasPrefix(source, "http://") {
		return nil, fmt.Errorf("insecure http:// URL refused for native engine artifact: %s", source)
	}
	if strings.HasPrefix(source, "file://") {
		u, err := url.Parse(source)
		if err != nil {
			return nil, err
		}
		source = u.Path
	}
	// #nosec G304 -- local path explicitly provided by release selection or manifest.
	return os.ReadFile(source)
}

func extractLibraryFromTarGz(archive []byte, libraryName string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open engine archive gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read engine archive tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if filepath.Base(hdr.Name) == libraryName {
			data, err := io.ReadAll(io.LimitReader(tr, maxEngineArtifactBytes+1))
			if err != nil {
				return nil, err
			}
			if int64(len(data)) > maxEngineArtifactBytes {
				return nil, fmt.Errorf("extracted library %s exceeds size limit", libraryName)
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("engine archive does not contain %q", libraryName)
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".pw-py-engine-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func isHTTPSURL(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "https://")
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
