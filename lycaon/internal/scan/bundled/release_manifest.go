package bundled

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"gopkg.in/yaml.v3"
)

// ReleaseArtifact pins the distribution archive containing the executable and provenance.
type ReleaseArtifact struct {
	GOOS   string `yaml:"goos" json:"goos"`
	GOARCH string `yaml:"goarch" json:"goarch"`
	URL    string `yaml:"url" json:"url"`
	SHA256 string `yaml:"sha256" json:"sha256"`
	Bytes  int64  `yaml:"bytes" json:"bytes"`
}

func validateReleaseArtifacts(artifacts []ReleaseArtifact) error {
	if len(artifacts) == 0 {
		return fmt.Errorf("opengrep requires pinned prebuilt release artifacts")
	}
	seen := make(map[string]bool)
	for _, artifact := range artifacts {
		platform := artifact.GOOS + "/" + artifact.GOARCH
		if (artifact.GOOS != "darwin" && artifact.GOOS != "linux") || !supportedPlatform(artifact.GOOS, artifact.GOARCH) || seen[platform] {
			return fmt.Errorf("invalid or duplicate release platform: %s", platform)
		}
		if !validHex(artifact.SHA256, 32) || artifact.Bytes <= 0 || artifact.Bytes > maxArtifactBytes {
			return fmt.Errorf("invalid release digest or size for %s", platform)
		}
		if err := validateReleaseURL(artifact.URL); err != nil {
			return err
		}
		seen[platform] = true
	}
	return nil
}

func validateReleaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" {
		return fmt.Errorf("release URL must be an absolute HTTPS URL without credentials or fragment")
	}
	return nil
}

// ReleaseForPlatform returns the checked-in distribution pin for a target.
func (m *Manifest) ReleaseForPlatform(goos, goarch string) (*ReleaseArtifact, error) {
	if err := ValidateManifest(m); err != nil {
		return nil, err
	}
	if m.candidate != nil {
		return nil, fmt.Errorf("experimental candidates have no release artifact")
	}
	for _, artifact := range m.OpenGrep.Artifacts {
		if artifact.GOOS == goos && artifact.GOARCH == goarch {
			return &artifact, nil
		}
	}
	return nil, fmt.Errorf("no prebuilt opengrep release for %s/%s", goos, goarch)
}

// ParseReleaseManifest accepts one complete, unambiguous release descriptor.
func ParseReleaseManifest(raw []byte) (*Manifest, error) {
	if len(raw) == 0 || len(raw) > maxReleaseManifestBytes {
		return nil, fmt.Errorf("release manifest size is invalid")
	}
	if err := rejectDuplicateJSONFields(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode release manifest: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("release manifest must contain one object")
	}
	if err := ValidateManifest(&manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func rejectDuplicateJSONFields(decoder *json.Decoder, depth int) error {
	if depth > 32 {
		return fmt.Errorf("release manifest nesting exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return fmt.Errorf("invalid JSON delimiter")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		if delim == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] || name != strings.ToLower(name) {
				return fmt.Errorf("duplicate or invalid release manifest field")
			}
			seen[name] = true
		}
		if err := rejectDuplicateJSONFields(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

// WriteReleaseManifest atomically changes the pinned selection after artifact admission.
func WriteReleaseManifest(ctx context.Context, m *Manifest, destination string) error {
	if err := ValidateManifest(m); err != nil {
		return err
	}
	if m.identity == nil || m.artifactDirectory == "" || !m.releaseAdmitted {
		return fmt.Errorf("release selection requires an admitted artifact")
	}
	if err := m.verifyReleaseSelection(ctx); err != nil {
		return err
	}
	raw, err := yaml.Marshal(struct {
		OpenGrep  OpenGrepManifest           `yaml:"opengrep"`
		Selection *ReleaseSelectionReference `yaml:"opengrep_selection,omitempty"`
	}{m.OpenGrep, m.OpenGrepSelection})
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: filepath.Dir(absolute), Rel: filepath.Base(absolute)}, Source: bytes.NewReader(raw), Mode: 0o644})
	return err
}
