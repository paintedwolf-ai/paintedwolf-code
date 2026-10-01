package bundled

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/lycaon/lycaon/config"
)

const ImplOpengrep = "opengrep"

// Manifest pins a maintained release and carries the build-embedded artifact authority.
type Manifest struct {
	OpenGrep          OpenGrepManifest           `yaml:"opengrep" json:"opengrep"`
	OpenGrepSelection *ReleaseSelectionReference `yaml:"opengrep_selection,omitempty" json:"-"`
	candidate         *localCandidate
	identity          *BuildIdentity
	artifactDirectory string
	releaseAdmitted   bool
}

type OpenGrepManifest struct {
	Version              string            `yaml:"version" json:"version"`
	Origin               string            `yaml:"origin" json:"origin"`
	UpstreamVersion      string            `yaml:"upstream_version" json:"upstream_version"`
	BaseRevision         string            `yaml:"base_revision" json:"base_revision"`
	Revision             uint64            `yaml:"revision" json:"revision"`
	SourceLockSHA256     string            `yaml:"source_lock_sha256" json:"source_lock_sha256"`
	SourceManifestSHA256 string            `yaml:"source_manifest_sha256" json:"source_manifest_sha256"`
	License              string            `yaml:"license" json:"license"`
	Artifacts            []ReleaseArtifact `yaml:"artifacts" json:"artifacts"`
}

// PlatformArtifact pins one binary for an OS and architecture.
type PlatformArtifact struct {
	GOOS        string `yaml:"goos"`
	GOARCH      string `yaml:"goarch"`
	SHA256      string `yaml:"sha256"`
	BinaryBytes int64  `yaml:"binary_bytes"`
}

// LoadManifest returns the bundled artifact manifest.
func LoadManifest() (*Manifest, error) {
	if directory := os.Getenv(EnvOpenGrepCandidate); directory != "" {
		return loadCandidate(directory)
	}
	data, err := config.Read(config.BundledScanners)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := config.DecodeYAML(data, &m); err != nil {
		return nil, fmt.Errorf("parse bundled manifest: %w", err)
	}
	if err := ValidateManifest(&m); err != nil {
		return nil, err
	}
	if buildIdentityBase64 != "" {
		return ManifestWithBuildIdentity(&m, buildIdentityBase64)
	}
	return &m, nil
}

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$`)

// ValidateManifest checks source identity and exact platform artifact pins.
func ValidateManifest(m *Manifest) error {
	if m == nil {
		return fmt.Errorf("manifest is nil")
	}
	engine := m.OpenGrep
	if !versionPattern.MatchString(engine.Version) || !versionPattern.MatchString(engine.UpstreamVersion) {
		return fmt.Errorf("opengrep version and upstream_version must be version identifiers")
	}
	if !validHex(engine.BaseRevision, 20) {
		return fmt.Errorf("opengrep.base_revision must be a full Git revision")
	}
	expected := fmt.Sprintf("%s+paintedwolf.%d", engine.UpstreamVersion, engine.Revision)
	if engine.Origin != "downstream" || engine.Revision == 0 || engine.Version != expected || !validHex(engine.SourceLockSHA256, 32) {
		return fmt.Errorf("maintained source requires downstream origin, ordered revision, matching version, and source lock digest")
	}
	if strings.TrimSpace(engine.License) == "" {
		return fmt.Errorf("opengrep.license required")
	}
	if m.candidate != nil {
		return m.candidate.validateManifest(engine)
	}
	if !validHex(engine.SourceManifestSHA256, 32) {
		return fmt.Errorf("maintained source requires a source manifest digest")
	}
	if err := validateReleaseArtifacts(engine.Artifacts); err != nil {
		return err
	}
	if m.OpenGrepSelection != nil {
		if err := m.OpenGrepSelection.validate(engine.Version); err != nil {
			return err
		}
	}
	if m.identity != nil {
		return m.identity.validate(engine)
	}
	return nil
}

func validHex(value string, size int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == size && value == strings.ToLower(value)
}

func supportedPlatform(goos, goarch string) bool {
	return ((goos == "darwin" || goos == "linux") && (goarch == "amd64" || goarch == "arm64")) || (goos == "windows" && goarch == "amd64")
}

// ArtifactForCurrentPlatform returns the manifest row for runtime.GOOS/GOARCH.
func (m *Manifest) ArtifactForCurrentPlatform() (*PlatformArtifact, error) {
	return m.ArtifactForPlatform(runtime.GOOS, runtime.GOARCH)
}

func (m *Manifest) ArtifactForPlatform(goos, goarch string) (*PlatformArtifact, error) {
	if err := ValidateManifest(m); err != nil {
		return nil, err
	}
	if m.candidate != nil {
		p := m.candidate.provenance
		if goos == runtime.GOOS && goarch == runtime.GOARCH {
			return &PlatformArtifact{GOOS: goos, GOARCH: goarch, SHA256: p.BinarySHA256, BinaryBytes: p.BinaryBytes}, nil
		}
	}
	if m.identity == nil {
		return nil, fmt.Errorf("opengrep build identity is absent; build the sidecar with the resolved maintained artifact identity")
	}
	if m.identity.GOOS == goos && m.identity.GOARCH == goarch {
		return &PlatformArtifact{GOOS: goos, GOARCH: goarch, SHA256: m.identity.BinarySHA256, BinaryBytes: m.identity.BinaryBytes}, nil
	}
	return nil, fmt.Errorf("no opengrep build identity for %s/%s", goos, goarch)
}

// EngineBundledDir returns the staged engine directory.
func EngineBundledDir(stageRoot, version string) string {
	return filepath.Join(stageRoot, "bundled", "opengrep-"+version)
}

// BinaryPath returns the engine binary path inside extractDir.
func BinaryPath(extractDir string) string {
	name := "opengrep"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(extractDir, name)
}

// VerifyFileSHA256 compares file contents to expected hex digest.
func VerifyFileSHA256(path, expectedHex string) error {
	expected := normalizeSHA256(expectedHex)
	if !validHex(expected, 32) {
		return fmt.Errorf("invalid expected artifact SHA-256")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("artifact is not a regular file: %s", path)
	}
	// #nosec G304 -- path names the selected bundled scanner.
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expected {
		return fmt.Errorf("sha256 mismatch for %s: got %s want %s", path, got, expected)
	}
	return nil
}

func normalizeSHA256(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
