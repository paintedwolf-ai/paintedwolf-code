package pythonruntime

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	portablepythonconfig "github.com/lycaon/lycaon/config/runtime/portable-python"
	"github.com/lycaon/lycaon/internal/pythonnative"
	"gopkg.in/yaml.v3"
)

const (
	// ExpectedNativeEngineABIVersion is the C ABI version required by this consumer.
	ExpectedNativeEngineABIVersion uint32 = 1

	// ExpectedNativeEngineImportCount is the exact WASI import count for CPython 3.12.0/WASI.
	ExpectedNativeEngineImportCount uint32 = 43

	// ExpectedNativeEngineABIHeaderSHA256 is the pinned SHA-256 of pw_py_engine.h.
	ExpectedNativeEngineABIHeaderSHA256 = "9ec5afc672cd5d76cf184b69ae37276f2becebd8a71276d64fda7f986f1d5dbd"

	// ExpectedNativeEngineImportInventorySHA256 is the pinned SHA-256 of PY07 imports.json.
	ExpectedNativeEngineImportInventorySHA256 = "d4dca3e9def0586bdc7c43af04340073fcbd99b28a87f26f33341b30f00d623b"

	// EngineReleaseDescriptorSchemaVersion is PY07's release manifest schema version.
	EngineReleaseDescriptorSchemaVersion = "paintedwolf.native-python-engine-release/v1"

	// EngineArtifactManifestSchemaVersion is the per-platform staged artifact-manifest.json schema version.
	EngineArtifactManifestSchemaVersion = "paintedwolf.native-python-engine-artifact/v1"

	// EnvEngineDir overrides the staged native engine directory location.
	EnvEngineDir = "PW_PORTABLE_PYTHON_ENGINE_DIR"

	// EnvEngineManifest overrides the bundled-manifest.yaml path for tooling/tests.
	EnvEngineManifest = "PW_PORTABLE_PYTHON_ENGINE_MANIFEST"

	// EnvArtifactDir optionally points staging at a local directory containing
	// verified retained producer artifacts (PY07 native engine archives and
	// PY08 checked-hash .pyc image tarballs).
	EnvArtifactDir = "PW_PORTABLE_PYTHON_ARTIFACT_DIR"
)

// engineBuildIdentityBase64 may be stamped at link time via -ldflags
// "-X github.com/lycaon/lycaon/internal/pythonruntime.engineBuildIdentityBase64=...".
var engineBuildIdentityBase64 string

// BundledEngineManifest pins a maintained PY07 native CPython/WASI AOT engine
// release and its paired checked-hash CPython 3.12 package image following the
// OpenGrep bundled-manifest.yaml convention.
type BundledEngineManifest struct {
	Engine    EngineReleaseSpec         `yaml:"portable_python_engine" json:"portable_python_engine"`
	Image     *BundledImageSpec         `yaml:"portable_python_image,omitempty" json:"portable_python_image,omitempty"`
	Selection *EngineSelectionReference `yaml:"portable_python_engine_selection,omitempty" json:"portable_python_engine_selection,omitempty"`
	identity  *EngineBuildIdentity
}

// BundledImageSpec pins the verified deterministic checked-hash CPython 3.12
// package image paired with the native engine release.
type BundledImageSpec struct {
	ImageID                  string `yaml:"image_id" json:"image_id"`
	ArchiveName              string `yaml:"archive_name" json:"archive_name"`
	ArchiveSHA256            string `yaml:"archive_sha256" json:"archive_sha256"`
	ArchiveBytes             int64  `yaml:"archive_bytes,omitempty" json:"archive_bytes,omitempty"`
	ManifestSHA256           string `yaml:"manifest_sha256" json:"manifest_sha256"`
	PackagesTreeSHA256       string `yaml:"packages_tree_sha256,omitempty" json:"packages_tree_sha256,omitempty"`
	CPythonWasmSHA256        string `yaml:"cpython_wasm_sha256" json:"cpython_wasm_sha256"`
	MeteredWasmSHA256        string `yaml:"metered_wasm_sha256" json:"metered_wasm_sha256"`
	ImportInventorySHA256    string `yaml:"import_inventory_sha256" json:"import_inventory_sha256"`
	BytecodeInvalidationMode string `yaml:"bytecode_invalidation_mode" json:"bytecode_invalidation_mode"`
	BytecodeMagicHex         string `yaml:"bytecode_magic_hex" json:"bytecode_magic_hex"`
	PycCount                 int    `yaml:"pyc_count" json:"pyc_count"`
	PycTreeSHA256            string `yaml:"pyc_tree_sha256" json:"pyc_tree_sha256"`
	URL                      string `yaml:"url" json:"url"`
}

// EngineReleaseSpec describes one pinned PY07 native engine release.
type EngineReleaseSpec struct {
	Version               string                  `yaml:"version" json:"version"`
	Origin                string                  `yaml:"origin" json:"origin"`
	UpstreamVersion       string                  `yaml:"upstream_version" json:"upstream_version"`
	BaseRevision          string                  `yaml:"base_revision" json:"base_revision"`
	Revision              uint64                  `yaml:"revision" json:"revision"`
	ABIVersion            uint32                  `yaml:"abi_version" json:"abi_version"`
	ABIHeaderSHA256       string                  `yaml:"abi_header_sha256" json:"abi_header_sha256"`
	CPythonWasmSHA256     string                  `yaml:"cpython_wasm_sha256" json:"cpython_wasm_sha256"`
	MeteredWasmSHA256     string                  `yaml:"metered_wasm_sha256" json:"metered_wasm_sha256"`
	ImportInventoryCount  uint32                  `yaml:"import_inventory_count" json:"import_inventory_count"`
	ImportInventorySHA256 string                  `yaml:"import_inventory_sha256" json:"import_inventory_sha256"`
	License               string                  `yaml:"license" json:"license"`
	Artifacts             []EngineReleaseArtifact `yaml:"artifacts" json:"artifacts"`
}

// EngineReleaseArtifact pins one platform shared library in a release.
type EngineReleaseArtifact struct {
	GOOS             string `yaml:"goos" json:"goos"`
	GOARCH           string `yaml:"goarch" json:"goarch"`
	LibraryName      string `yaml:"library_name" json:"library_name"`
	ArchiveName      string `yaml:"archive_name,omitempty" json:"archive_name,omitempty"`
	URL              string `yaml:"url" json:"url"`
	SHA256           string `yaml:"sha256" json:"sha256"`
	Bytes            int64  `yaml:"bytes,omitempty" json:"bytes,omitempty"`
	ArchiveSHA256    string `yaml:"archive_sha256,omitempty" json:"archive_sha256,omitempty"`
	ArchiveBytes     int64  `yaml:"archive_bytes,omitempty" json:"archive_bytes,omitempty"`
	CodesignHardened bool   `yaml:"codesign_hardened,omitempty" json:"codesign_hardened,omitempty"`
}

// EngineSelectionReference records the authenticated release tag and producer
// commit selected by scripts/select-portable-python-engine.sh.
type EngineSelectionReference struct {
	Tag              string `yaml:"tag" json:"tag"`
	ProducerCommit   string `yaml:"producer_commit" json:"producer_commit"`
	DescriptorSHA256 string `yaml:"descriptor_sha256" json:"descriptor_sha256"`
	EvidenceSHA256   string `yaml:"evidence_sha256,omitempty" json:"evidence_sha256,omitempty"`
}

// EngineArtifactManifest is the per-platform staged artifact-manifest.json
// accompanying libpw_cpython_wasi.{dylib,so}.
type EngineArtifactManifest struct {
	SchemaVersion         string `json:"schema_version"`
	Version               string `json:"version"`
	EngineTag             string `json:"engine_tag,omitempty"`
	SourceCommit          string `json:"source_commit,omitempty"`
	GOOS                  string `json:"goos"`
	GOARCH                string `json:"goarch"`
	Platform              string `json:"platform"`
	ABIVersion            uint32 `json:"abi_version"`
	ABIHeaderSHA256       string `json:"abi_header_sha256"`
	LibraryName           string `json:"library_name"`
	LibrarySHA256         string `json:"library_sha256"`
	LibraryBytes          int64  `json:"library_bytes,omitempty"`
	CPythonWasmSHA256     string `json:"cpython_wasm_sha256"`
	MeteredWasmSHA256     string `json:"metered_wasm_sha256"`
	ImportInventoryCount  uint32 `json:"import_inventory_count"`
	ImportInventorySHA256 string `json:"import_inventory_sha256"`
}

// EngineBuildIdentity is the build-time stamped identity passed via ldflags.
type EngineBuildIdentity struct {
	Version               string `json:"version"`
	GOOS                  string `json:"goos"`
	GOARCH                string `json:"goarch"`
	ABIVersion            uint32 `json:"abi_version"`
	LibraryName           string `json:"library_name"`
	LibrarySHA256         string `json:"library_sha256"`
	LibraryBytes          int64  `json:"library_bytes,omitempty"`
	CPythonWasmSHA256     string `json:"cpython_wasm_sha256"`
	MeteredWasmSHA256     string `json:"metered_wasm_sha256"`
	ImportInventorySHA256 string `json:"import_inventory_sha256"`
}

// EngineReleaseDescriptor matches PY07's paintedwolf.native-python-engine-release/v1
// JSON schema (manifest.fixture.json).
type EngineReleaseDescriptor struct {
	SchemaVersion   string `json:"schema_version"`
	EngineTag       string `json:"engine_tag"`
	SourceCommit    string `json:"source_commit"`
	ABIVersion      uint32 `json:"abi_version"`
	ABIHeaderSHA256 string `json:"abi_header_sha256"`
	CPython         struct {
		Version    string `json:"version"`
		ReleaseTag string `json:"release_tag"`
		WasiSDK    string `json:"wasi_sdk"`
		WasmSHA256 string `json:"wasm_sha256"`
	} `json:"cpython"`
	Metering struct {
		AlgorithmVersion  string `json:"algorithm_version"`
		MeteredWasmSHA256 string `json:"metered_wasm_sha256"`
	} `json:"metering"`
	WABT struct {
		Version        string   `json:"version"`
		GeneratorFlags []string `json:"generator_flags"`
		Patches        []string `json:"patches"`
	} `json:"wabt"`
	ImportInventory struct {
		Count  uint32 `json:"count"`
		SHA256 string `json:"sha256"`
	} `json:"import_inventory"`
	Limits struct {
		DefaultMaxMemoryPages        uint32 `json:"default_max_memory_pages"`
		WasmPageBytes                uint32 `json:"wasm_page_bytes"`
		AsyncCancelSupported         bool   `json:"async_cancel_supported"`
		ConcurrentInstancesSupported bool   `json:"concurrent_instances_supported"`
	} `json:"limits"`
	Artifacts      map[string]EngineReleaseDescriptorArtifact `json:"artifacts"`
	License        string                                     `json:"license,omitempty"`
	EvidenceSHA256 string                                     `json:"evidence_sha256,omitempty"`
	Revision       uint64                                     `json:"revision,omitempty"`
}

// EngineReleaseDescriptorArtifact is one platform entry in PY07's release manifest.
type EngineReleaseDescriptorArtifact struct {
	Platform         string   `json:"platform"`
	ArchiveName      string   `json:"archive_name"`
	ArchiveSHA256    string   `json:"archive_sha256"`
	ArchiveBytes     int64    `json:"archive_bytes,omitempty"`
	LibraryName      string   `json:"library_name"`
	LibrarySHA256    string   `json:"library_sha256"`
	LibraryBytes     int64    `json:"library_bytes,omitempty"`
	URL              string   `json:"url,omitempty"`
	CodesignHardened bool     `json:"codesign_hardened"`
	JITEntitlements  []string `json:"jit_entitlements"`
}

var engineVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$`)

var supportedNativeEngineLibraries = map[string]string{
	"darwin/arm64": "libpw_cpython_wasi.dylib",
	"linux/amd64":  "libpw_cpython_wasi.so",
}

// LibraryNameForPlatform returns the canonical shared library filename for an
// explicitly supported (goos, goarch) target, independent of the running host.
// Unknown operating systems or architectures are rejected explicitly instead of
// defaulting to a Linux-style filename.
func LibraryNameForPlatform(goos, goarch string) (string, error) {
	key := strings.TrimSpace(goos) + "/" + strings.TrimSpace(goarch)
	name, ok := supportedNativeEngineLibraries[key]
	if !ok {
		return "", fmt.Errorf("unsupported portable python native engine platform %q", key)
	}
	return name, nil
}

// EnginePlatformForTarget maps a packaging target triple to GOOS/GOARCH.
func EnginePlatformForTarget(target string) (string, string, error) {
	switch strings.TrimSpace(target) {
	case "aarch64-apple-darwin", "darwin-arm64", "darwin/arm64":
		return "darwin", "arm64", nil
	case "x86_64-unknown-linux-gnu", "linux-amd64", "linux/amd64":
		return "linux", "amd64", nil
	default:
		return "", "", fmt.Errorf("unsupported portable python native engine target %q", target)
	}
}

// LoadBundledEngineManifest loads the build-embedded bundled-manifest.yaml (or
// PW_PORTABLE_PYTHON_ENGINE_MANIFEST when explicitly set) and applies any
// link-time build identity.
func LoadBundledEngineManifest() (*BundledEngineManifest, error) {
	if override := strings.TrimSpace(os.Getenv(EnvEngineManifest)); override != "" {
		return LoadBundledEngineManifestFile(override)
	}
	return ParseBundledEngineManifest(portablepythonconfig.BundledManifestYAML)
}

// LoadBundledEngineManifestFile reads and parses a bundled-manifest.yaml file.
func LoadBundledEngineManifestFile(path string) (*BundledEngineManifest, error) {
	// #nosec G304 -- explicit manifest path from caller or config.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read portable python engine manifest %s: %w", path, err)
	}
	return ParseBundledEngineManifest(data)
}

// ParseBundledEngineManifest decodes YAML bytes into a BundledEngineManifest.
func ParseBundledEngineManifest(data []byte) (*BundledEngineManifest, error) {
	var m BundledEngineManifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse portable python engine manifest: %w", err)
	}
	if engineBuildIdentityBase64 != "" {
		withID, err := ManifestWithEngineBuildIdentity(&m, engineBuildIdentityBase64)
		if err != nil {
			return nil, err
		}
		return withID, nil
	}
	return &m, nil
}

// ValidateBundledEngineManifest strictly validates that m pins a complete,
// authenticated PY07 release for supported platforms.
func ValidateBundledEngineManifest(m *BundledEngineManifest) error {
	if m == nil {
		return errors.New("portable python engine manifest is nil")
	}
	e := m.Engine
	if !engineVersionPattern.MatchString(e.Version) || !engineVersionPattern.MatchString(e.UpstreamVersion) {
		return fmt.Errorf("portable python engine version %q and upstream_version %q must be valid version identifiers", e.Version, e.UpstreamVersion)
	}
	expectedVer := fmt.Sprintf("%s+paintedwolf.%d", e.UpstreamVersion, e.Revision)
	if e.Origin != "downstream" || e.Revision == 0 || e.Version != expectedVer {
		return fmt.Errorf("portable python engine requires downstream origin, positive revision, and version %q (got %q)", expectedVer, e.Version)
	}
	if !isLowerHexLen(e.BaseRevision, 40) {
		return fmt.Errorf("portable python engine base_revision must be a 40-char lowercase git SHA (got %q)", e.BaseRevision)
	}
	if e.ABIVersion != ExpectedNativeEngineABIVersion {
		return fmt.Errorf("portable python engine abi_version %d does not match expected %d", e.ABIVersion, ExpectedNativeEngineABIVersion)
	}
	if !isLowerHexLen(e.ABIHeaderSHA256, 64) {
		return fmt.Errorf("portable python engine abi_header_sha256 must be a 64-char lowercase sha256")
	}
	if !strings.EqualFold(e.ABIHeaderSHA256, ExpectedNativeEngineABIHeaderSHA256) {
		return fmt.Errorf("portable python engine abi_header_sha256 mismatch: got %s want %s", e.ABIHeaderSHA256, ExpectedNativeEngineABIHeaderSHA256)
	}
	if !isLowerHexLen(e.CPythonWasmSHA256, 64) {
		return fmt.Errorf("portable python engine cpython_wasm_sha256 must be a 64-char lowercase sha256")
	}
	if !isLowerHexLen(e.MeteredWasmSHA256, 64) {
		return fmt.Errorf("portable python engine metered_wasm_sha256 must be a 64-char lowercase sha256")
	}
	if e.ImportInventoryCount != ExpectedNativeEngineImportCount {
		return fmt.Errorf("portable python engine import_inventory_count %d does not match expected %d", e.ImportInventoryCount, ExpectedNativeEngineImportCount)
	}
	if !isLowerHexLen(e.ImportInventorySHA256, 64) {
		return fmt.Errorf("portable python engine import_inventory_sha256 must be a 64-char lowercase sha256")
	}
	if strings.TrimSpace(e.License) == "" {
		return errors.New("portable python engine license is required")
	}
	if len(e.Artifacts) == 0 {
		return errors.New("portable python engine manifest must declare at least one platform artifact")
	}
	seen := make(map[string]struct{}, len(e.Artifacts))
	for _, art := range e.Artifacts {
		key := art.GOOS + "/" + art.GOARCH
		if _, err := LibraryNameForPlatform(art.GOOS, art.GOARCH); err != nil {
			return err
		}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("duplicate portable python engine artifact for %s", key)
		}
		seen[key] = struct{}{}
		if strings.TrimSpace(art.LibraryName) == "" {
			return fmt.Errorf("portable python engine artifact for %s missing library_name", key)
		}
		if !isLowerHexLen(art.SHA256, 64) || isAllZerosHex(art.SHA256) {
			return fmt.Errorf("portable python engine artifact for %s missing pinned non-zero sha256", key)
		}
		if strings.TrimSpace(art.URL) == "" {
			return fmt.Errorf("portable python engine artifact for %s missing url/path", key)
		}
	}
	if m.Selection != nil && strings.TrimSpace(m.Selection.Tag) != "" {
		if !isLowerHexLen(m.Selection.ProducerCommit, 40) {
			return fmt.Errorf("portable python engine selection producer_commit must be a 40-char lowercase git SHA")
		}
		if !isLowerHexLen(m.Selection.DescriptorSHA256, 64) {
			return fmt.Errorf("portable python engine selection descriptor_sha256 must be a 64-char lowercase sha256")
		}
	}
	if m.Image != nil {
		if err := ValidateBundledImageSpec(m.Image, &m.Engine); err != nil {
			return err
		}
	}
	return nil
}

// ValidateBundledImageSpec validates the pinned checked-hash .pyc image spec
// and confirms its CPython/metered/import digests match the paired native engine.
func ValidateBundledImageSpec(img *BundledImageSpec, eng *EngineReleaseSpec) error {
	if img == nil {
		return errors.New("portable python bundled image spec is nil")
	}
	if strings.TrimSpace(img.ImageID) == "" {
		return errors.New("portable python bundled image missing image_id")
	}
	if strings.TrimSpace(img.ArchiveName) == "" {
		return errors.New("portable python bundled image missing archive_name")
	}
	if !isLowerHexLen(img.ArchiveSHA256, 64) || isAllZerosHex(img.ArchiveSHA256) {
		return errors.New("portable python bundled image missing pinned non-zero archive_sha256")
	}
	if !isLowerHexLen(img.ManifestSHA256, 64) || isAllZerosHex(img.ManifestSHA256) {
		return errors.New("portable python bundled image missing pinned non-zero manifest_sha256")
	}
	if img.BytecodeInvalidationMode != "checked-hash" {
		return fmt.Errorf("portable python bundled image bytecode_invalidation_mode %q must be checked-hash", img.BytecodeInvalidationMode)
	}
	if !strings.EqualFold(img.BytecodeMagicHex, CPython312BytecodeMagicHex) {
		return fmt.Errorf("portable python bundled image bytecode_magic_hex %q must be %s", img.BytecodeMagicHex, CPython312BytecodeMagicHex)
	}
	if img.PycCount <= 0 {
		return fmt.Errorf("portable python bundled image pyc_count must be positive (got %d)", img.PycCount)
	}
	if !isLowerHexLen(img.PycTreeSHA256, 64) || isAllZerosHex(img.PycTreeSHA256) {
		return errors.New("portable python bundled image missing pinned non-zero pyc_tree_sha256")
	}
	if eng != nil {
		if !strings.EqualFold(img.CPythonWasmSHA256, eng.CPythonWasmSHA256) {
			return fmt.Errorf("bundled image cpython_wasm_sha256 %s != engine %s", img.CPythonWasmSHA256, eng.CPythonWasmSHA256)
		}
		if !strings.EqualFold(img.MeteredWasmSHA256, eng.MeteredWasmSHA256) {
			return fmt.Errorf("bundled image metered_wasm_sha256 %s != engine %s", img.MeteredWasmSHA256, eng.MeteredWasmSHA256)
		}
		if !strings.EqualFold(img.ImportInventorySHA256, eng.ImportInventorySHA256) {
			return fmt.Errorf("bundled image import_inventory_sha256 %s != engine %s", img.ImportInventorySHA256, eng.ImportInventorySHA256)
		}
	}
	return nil
}

// ArtifactForPlatform returns the pinned artifact for goos/goarch.
func (m *BundledEngineManifest) ArtifactForPlatform(goos, goarch string) (*EngineReleaseArtifact, error) {
	if err := ValidateBundledEngineManifest(m); err != nil {
		return nil, err
	}
	if m.identity != nil && m.identity.GOOS == goos && m.identity.GOARCH == goarch {
		return &EngineReleaseArtifact{
			GOOS:        m.identity.GOOS,
			GOARCH:      m.identity.GOARCH,
			LibraryName: m.identity.LibraryName,
			SHA256:      m.identity.LibrarySHA256,
			Bytes:       m.identity.LibraryBytes,
		}, nil
	}
	for i := range m.Engine.Artifacts {
		art := &m.Engine.Artifacts[i]
		if art.GOOS == goos && art.GOARCH == goarch {
			cp := *art
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("no portable python native engine artifact pinned for %s/%s", goos, goarch)
}

// ArtifactForCurrentPlatform returns the pinned artifact for the running host platform.
func (m *BundledEngineManifest) ArtifactForCurrentPlatform() (*EngineReleaseArtifact, error) {
	goos, goarch := pythonnative.HostPlatform()
	return m.ArtifactForPlatform(goos, goarch)
}

// EncodedBuildIdentity encodes the platform's build identity as base64 JSON
// for -ldflags stamping.
func (m *BundledEngineManifest) EncodedBuildIdentity(goos, goarch string) (string, error) {
	art, err := m.ArtifactForPlatform(goos, goarch)
	if err != nil {
		return "", err
	}
	id := EngineBuildIdentity{
		Version:               m.Engine.Version,
		GOOS:                  art.GOOS,
		GOARCH:                art.GOARCH,
		ABIVersion:            m.Engine.ABIVersion,
		LibraryName:           art.LibraryName,
		LibrarySHA256:         strings.ToLower(strings.TrimSpace(art.SHA256)),
		LibraryBytes:          art.Bytes,
		CPythonWasmSHA256:     strings.ToLower(strings.TrimSpace(m.Engine.CPythonWasmSHA256)),
		MeteredWasmSHA256:     strings.ToLower(strings.TrimSpace(m.Engine.MeteredWasmSHA256)),
		ImportInventorySHA256: strings.ToLower(strings.TrimSpace(m.Engine.ImportInventorySHA256)),
	}
	raw, err := json.Marshal(id)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// ManifestWithEngineBuildIdentity decodes a base64 build identity and binds it
// to a copy of m after verifying consistency with m.Engine.
func ManifestWithEngineBuildIdentity(m *BundledEngineManifest, encoded string) (*BundledEngineManifest, error) {
	if m == nil {
		return nil, errors.New("manifest is nil")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("decode engine build identity: %w", err)
	}
	var id EngineBuildIdentity
	if err := json.Unmarshal(raw, &id); err != nil {
		return nil, fmt.Errorf("unmarshal engine build identity: %w", err)
	}
	if id.ABIVersion != m.Engine.ABIVersion ||
		!strings.EqualFold(id.CPythonWasmSHA256, m.Engine.CPythonWasmSHA256) ||
		!strings.EqualFold(id.MeteredWasmSHA256, m.Engine.MeteredWasmSHA256) ||
		!strings.EqualFold(id.ImportInventorySHA256, m.Engine.ImportInventorySHA256) {
		return nil, fmt.Errorf("engine build identity does not match bundled manifest digests")
	}
	cp := *m
	cp.identity = &id
	return &cp, nil
}

// LoadEngineArtifactManifest reads and parses an artifact-manifest.json file.
func LoadEngineArtifactManifest(path string) (*EngineArtifactManifest, error) {
	// #nosec G304 -- explicit staged manifest path.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read engine artifact manifest %s: %w", path, err)
	}
	var am EngineArtifactManifest
	if err := json.Unmarshal(data, &am); err != nil {
		return nil, fmt.Errorf("decode engine artifact manifest %s: %w", path, err)
	}
	if am.SchemaVersion != EngineArtifactManifestSchemaVersion {
		return nil, fmt.Errorf("unsupported engine artifact manifest schema_version %q (want %q)", am.SchemaVersion, EngineArtifactManifestSchemaVersion)
	}
	return &am, nil
}

// StagedEngineDir returns the canonical staged engine directory under stageRoot.
func StagedEngineDir(stageRoot string) string {
	return filepath.Join(stageRoot, "portable-python", "engine")
}

// StagedImageDir returns the canonical staged image directory under stageRoot.
func StagedImageDir(stageRoot string) string {
	return filepath.Join(stageRoot, "portable-python", "image")
}

func isLowerHexLen(s string, n int) bool {
	if len(s) != n || s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func isAllZerosHex(s string) bool {
	if s == "" {
		return true
	}
	for i := 0; i < len(s); i++ {
		if s[i] != '0' {
			return false
		}
	}
	return true
}
