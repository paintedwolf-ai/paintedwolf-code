package extpacks

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

const DesiredFormat = 1

// EmptyDesired enables stock packs without overrides.
func EmptyDesired() DesiredState {
	return DesiredState{Format: DesiredFormat, Own: map[string]string{}}
}

// LoadDesiredFile validates one desired-state file.
func LoadDesiredFile(path string) (DesiredState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return EmptyDesired(), nil
		}
		return DesiredState{}, err
	}
	return ParseDesired(path, data)
}

// ValidateDesired rejects ambiguous installation and configuration state.
func ValidateDesired(d DesiredState) error {
	if d.Format != DesiredFormat {
		return &UnsupportedFormatError{Doc: "extensions.yaml", Field: "format", Got: d.Format, Want: DesiredFormat}
	}
	seen := map[string]bool{}
	for _, pack := range d.Packs {
		id := strings.TrimSpace(pack.ID)
		if err := ValidatePackID(id); err != nil {
			return fmt.Errorf("pack id: %w", err)
		}
		if seen[id] {
			return fmt.Errorf("duplicate pack %q", id)
		}
		seen[id] = true
		hasSource := strings.TrimSpace(pack.Source) != ""
		hasVersion := strings.TrimSpace(pack.Version) != ""
		hasRef := strings.TrimSpace(pack.Ref) != ""
		if !hasSource {
			if hasVersion || hasRef || pack.Development {
				return fmt.Errorf("pack %s has a resolution mode without source", id)
			}
			continue
		}
		if IsStockPackID(id) {
			return fmt.Errorf("stock package %s cannot declare an external source", id)
		}
		if pack.Development {
			if hasVersion || hasRef {
				return fmt.Errorf("pack %s development is mutually exclusive with version and ref", id)
			}
			continue
		}
		if hasVersion == hasRef {
			return fmt.Errorf("pack %s must select exactly one of version or ref", id)
		}
		if hasVersion {
			if _, err := semver.NewConstraint(pack.Version); err != nil {
				return fmt.Errorf("pack %s version: %w", id, err)
			}
		}
	}
	for unitID, packID := range d.Own {
		if strings.TrimSpace(unitID) == "" {
			return fmt.Errorf("own contains an empty unit id")
		}
		if err := ValidatePackID(strings.TrimSpace(packID)); err != nil {
			return fmt.Errorf("own %s: %w", unitID, err)
		}
	}
	return validateConfiguration(d.Configuration)
}

// validateConfiguration checks the configuration container.
func validateConfiguration(configuration map[string]map[string]any) error {
	for packID, properties := range configuration {
		if err := ValidatePackID(packID); err != nil {
			return fmt.Errorf("configuration pack: %w", err)
		}
		for name, value := range properties {
			if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
				return fmt.Errorf("configuration %s: property name %q is invalid", packID, name)
			}
			if err := validateConfigurationValue(value); err != nil {
				return fmt.Errorf("configuration %s.%s: %w", packID, name, err)
			}
		}
	}
	return nil
}

// validateConfigurationValue admits schema property values.
func validateConfigurationValue(value any) error {
	switch v := value.(type) {
	case bool, int, int64, uint64, float64, string:
		return nil
	case []any:
		for _, member := range v {
			if _, ok := member.(string); !ok {
				return fmt.Errorf("list values must be strings")
			}
		}
		return nil
	case []string:
		return nil
	default:
		return fmt.Errorf("value must be a scalar or a string list")
	}
}

// MergeDesired applies project unit disables to device state.
func MergeDesired(device DesiredState, projectDisabled []string) (DesiredState, DesiredProvenance) {
	out := EmptyDesired()
	if device.Format != 0 {
		out.Format = device.Format
	}

	prov := DesiredProvenance{
		Disabled: map[string]DesiredOrigin{},
	}

	for _, p := range device.Packs {
		id := strings.TrimSpace(p.ID)
		if id == "" {
			continue
		}
		p.ID = id
		out.Packs = append(out.Packs, p)
	}

	disabledSet := map[string]struct{}{}
	markDisabled := func(id string, origin DesiredOrigin) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := disabledSet[id]; ok {
			prov.Disabled[id] = OriginDevice
			return
		}
		disabledSet[id] = struct{}{}
		prov.Disabled[id] = origin
	}
	for _, id := range device.Disabled {
		markDisabled(id, OriginDevice)
	}
	for _, id := range projectDisabled {
		markDisabled(id, OriginProject)
	}
	for id := range disabledSet {
		out.Disabled = append(out.Disabled, id)
	}
	sort.Strings(out.Disabled)

	out.Own = map[string]string{}
	for k, v := range device.Own {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out.Own[k] = v
	}

	out.Declined = append([]string(nil), device.Declined...)
	sort.Strings(out.Declined)

	for packID, properties := range device.Configuration {
		for name, value := range properties {
			setConfigurationValue(&out, packID, name, value)
		}
	}
	return out, prov
}

func setConfigurationValue(d *DesiredState, packID, name string, value any) {
	if d.Configuration == nil {
		d.Configuration = map[string]map[string]any{}
	}
	if d.Configuration[packID] == nil {
		d.Configuration[packID] = map[string]any{}
	}
	d.Configuration[packID][name] = value
}

// LoadMergedDesired combines device state with project unit disables.
func LoadMergedDesired(projectDirs []string) (DesiredState, DesiredProvenance, error) {
	if err := settingsoverlay.CheckFormats(projectDirs); err != nil {
		return DesiredState{}, DesiredProvenance{}, err
	}
	devicePath, err := DeviceDesiredPath()
	if err != nil {
		return DesiredState{}, DesiredProvenance{}, err
	}
	device, err := LoadDesiredFile(devicePath)
	if err != nil {
		return DesiredState{}, DesiredProvenance{}, err
	}
	var projectDisabled []string
	disabled := map[string]struct{}{}
	seen := map[string]struct{}{}
	for _, projectDir := range projectDirs {
		projectDir = strings.TrimSpace(projectDir)
		if projectDir == "" {
			continue
		}
		if _, exists := seen[projectDir]; exists {
			continue
		}
		seen[projectDir] = struct{}{}
		suggestion, sugErr := LoadSuggestionFile(ProjectDesiredPath(projectDir))
		if sugErr != nil {
			return DesiredState{}, DesiredProvenance{}, sugErr
		}
		for _, id := range suggestion.Disabled {
			if id = strings.TrimSpace(id); id != "" {
				disabled[id] = struct{}{}
			}
		}
	}
	for id := range disabled {
		projectDisabled = append(projectDisabled, id)
	}
	sort.Strings(projectDisabled)
	merged, prov := MergeDesired(device, projectDisabled)
	return merged, prov, nil
}

// PackEnabled requires desired-state intent for non-stock packs.
func PackEnabled(desired DesiredState, packID string) bool {
	for _, p := range desired.Packs {
		if p.ID != packID {
			continue
		}
		if p.Enabled == nil {
			return true
		}
		return *p.Enabled
	}
	return IsStockPackID(packID)
}

// DesiredPackRow returns the desired row for packID if present.
func DesiredPackRow(desired DesiredState, packID string) (DesiredPack, bool) {
	for _, p := range desired.Packs {
		if p.ID == packID {
			return p, true
		}
	}
	return DesiredPack{}, false
}

// ProjectDesiredWithheldDiagnostic reports unapplied project extension state.
func ProjectDesiredWithheldDiagnostic(projectDir string) (Diagnostic, bool) {
	if strings.TrimSpace(projectDir) == "" {
		return Diagnostic{}, false
	}
	path := ProjectDesiredPath(projectDir)
	manifest, loadErr := LoadSuggestionFile(path)
	if loadErr != nil || len(manifest.Disabled) == 0 {
		return Diagnostic{}, false
	}
	return Diagnostic{
		Code:     DiagProjectTrustOff,
		Severity: SeverityForCode(DiagProjectTrustOff),
		Message: fmt.Sprintf(
			"This project's %s settings are not applying because Extension settings is off for the device or this project",
			DeviceDesiredName),
	}, true
}
