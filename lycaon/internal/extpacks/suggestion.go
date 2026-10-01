package extpacks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"gopkg.in/yaml.v3"
)

// SuggestionManifest is a project's extension proposals and unit disables.
type SuggestionManifest struct {
	Format   int             `yaml:"format"`
	Suggest  []SuggestedPack `yaml:"suggest,omitempty"`
	Disabled []string        `yaml:"disabled,omitempty"`
}

// SuggestedPack is one repo-authored install proposal.
type SuggestedPack struct {
	ID            string         `yaml:"id"`
	Source        string         `yaml:"source,omitempty"`
	Version       string         `yaml:"version,omitempty"`
	Ref           string         `yaml:"ref,omitempty"`
	Configuration map[string]any `yaml:"configuration,omitempty"`
}

// EmptySuggestion is a project with nothing to propose.
func EmptySuggestion() SuggestionManifest {
	return SuggestionManifest{Format: DesiredFormat}
}

// SuggestionRevision binds an install decision to the exact parsed proposal.
func SuggestionRevision(manifest SuggestionManifest) string {
	// Only install proposals affect review revisions.
	suggest := append([]SuggestedPack(nil), manifest.Suggest...)
	sort.Slice(suggest, func(i, j int) bool { return suggest[i].ID < suggest[j].ID })
	proposal := struct {
		Format  int             `json:"format"`
		Suggest []SuggestedPack `json:"suggest,omitempty"`
	}{Format: manifest.Format, Suggest: suggest}
	encoded, err := json.Marshal(proposal)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// EncodeSuggestion validates and canonically serializes one project file.
func EncodeSuggestion(m SuggestionManifest) ([]byte, error) {
	m.Format = DesiredFormat
	if err := ValidateSuggestion(m); err != nil {
		return nil, err
	}
	return yaml.Marshal(&m)
}

// DesiredFromSuggestion returns only project unit disables.
func DesiredFromSuggestion(m SuggestionManifest) DesiredState {
	disabled := make([]string, len(m.Disabled))
	copy(disabled, m.Disabled)
	return DesiredState{Format: m.Format, Disabled: disabled}
}

// SuggestionDiagnostics reports unique project suggestions.
func SuggestionDiagnostics(projectDirs []string) []Diagnostic {
	seenRoots := map[string]struct{}{}
	seenPacks := map[string]struct{}{}
	out := []Diagnostic{}
	for _, projectDir := range projectDirs {
		if strings.TrimSpace(projectDir) == "" {
			continue
		}
		if _, exists := seenRoots[projectDir]; exists {
			continue
		}
		seenRoots[projectDir] = struct{}{}
		m, err := LoadSuggestionFile(ProjectDesiredPath(projectDir))
		if err != nil {
			out = append(out, Diagnostic{
				Code:     DiagPackInvalid,
				Message:  err.Error(),
				Severity: SeverityForCode(DiagPackInvalid),
			})
			continue
		}
		for _, pack := range m.Suggest {
			id := strings.TrimSpace(pack.ID)
			if id == "" {
				continue
			}
			if _, exists := seenPacks[id]; exists {
				continue
			}
			seenPacks[id] = struct{}{}
			out = append(out, Diagnostic{
				Code:    DiagProjectPackSuggested,
				PackID:  id,
				Message: fmt.Sprintf("This project suggests %s; it is not installed from this file", id),
			})
		}
	}
	return StampSeverities(out)
}

// LoadSuggestionFile reads a project suggestion manifest. A missing file is empty.
func LoadSuggestionFile(path string) (SuggestionManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return EmptySuggestion(), nil
		}
		return SuggestionManifest{}, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return EmptySuggestion(), nil
	}
	return ParseSuggestion(path, data)
}

// SuggestionInvalidError is a suggestion document that does not decode or
// validate.
type SuggestionInvalidError struct {
	Source string
	Err    error
}

func (e *SuggestionInvalidError) Error() string { return e.Source + ": " + e.Err.Error() }

func (e *SuggestionInvalidError) Unwrap() error { return e.Err }

// ParseSuggestion strictly decodes one suggestion document.
func ParseSuggestion(source string, data []byte) (SuggestionManifest, error) {
	invalid := func(err error) (SuggestionManifest, error) {
		return SuggestionManifest{}, &SuggestionInvalidError{Source: source, Err: err}
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var m SuggestionManifest
	if err := dec.Decode(&m); err != nil {
		return invalid(err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return invalid(errors.New("multiple YAML documents are not allowed"))
		}
		return invalid(err)
	}
	if err := ValidateSuggestion(m); err != nil {
		return invalid(err)
	}
	return m, nil
}

// ValidateSuggestion rejects an unusable proposal without treating it as install intent.
func ValidateSuggestion(m SuggestionManifest) error {
	if m.Format != DesiredFormat {
		return &UnsupportedFormatError{Doc: "extensions.yaml", Field: "format", Got: m.Format, Want: DesiredFormat}
	}
	seen := map[string]bool{}
	for _, pack := range m.Suggest {
		id := strings.TrimSpace(pack.ID)
		if err := ValidatePackID(id); err != nil {
			return fmt.Errorf("suggest id: %w", err)
		}
		if seen[id] {
			return fmt.Errorf("duplicate suggest %q", id)
		}
		seen[id] = true
		if strings.TrimSpace(pack.Source) == "" {
			return fmt.Errorf("suggest %s needs a source", id)
		}
		if IsStockPackID(id) {
			return fmt.Errorf("stock package %s cannot be suggested", id)
		}
		hasVersion := strings.TrimSpace(pack.Version) != ""
		hasRef := strings.TrimSpace(pack.Ref) != ""
		if hasVersion && hasRef {
			return fmt.Errorf("suggest %s must select at most one of version or ref", id)
		}
		if hasVersion {
			if _, err := semver.NewConstraint(pack.Version); err != nil {
				return fmt.Errorf("suggest %s version: %w", id, err)
			}
		}
		if pack.Configuration != nil {
			if err := validateConfiguration(map[string]map[string]any{id: pack.Configuration}); err != nil {
				return err
			}
		}
	}
	return nil
}

// DeclinedSet is the device-wide declined pack ids.
func DeclinedSet(desired DesiredState) map[string]struct{} {
	out := make(map[string]struct{}, len(desired.Declined))
	for _, id := range desired.Declined {
		id = strings.TrimSpace(id)
		if id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}
