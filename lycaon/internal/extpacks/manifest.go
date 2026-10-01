package extpacks

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"gopkg.in/yaml.v3"
)

const (
	// ManifestVersion is the extension.yaml document format understood by this host.
	ManifestVersion = 1
	// ExtensionAPIVersion is the semantic contract implemented by pack discovery and resolve.
	ExtensionAPIVersion = "1.0.0"
)

var extensionCapabilities = map[string]struct{}{
	"agents": {}, "approvals": {}, "guidance": {}, "host.bindings": {},
	"host.user_notices": {}, "mcp.bindings": {}, "playbooks": {}, "policy": {},
	"shared": {}, "skills": {}, "tools.profiles": {}, "tools.schemas": {},
	"workflows": {}, "workflows.topologies": {},
	"host.credential_slots": {},
}

// ParseManifest strictly decodes and validates one extension manifest.
func ParseManifest(source string, data []byte) (Manifest, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var man Manifest
	if err := dec.Decode(&man); err != nil {
		return Manifest{}, fmt.Errorf("parse extension.yaml: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Manifest{}, fmt.Errorf("parse extension.yaml: multiple YAML documents are not allowed")
		}
		return Manifest{}, fmt.Errorf("parse extension.yaml: %w", err)
	}
	if err := ValidateManifest(man); err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", source, err)
	}
	return normalizeManifest(man), nil
}

// ValidateManifest enforces the author-facing package contract.
func ValidateManifest(man Manifest) error {
	if man.ManifestVersion != ManifestVersion {
		return &UnsupportedFormatError{Doc: "extension.yaml", Field: "manifest_version", Got: man.ManifestVersion, Want: ManifestVersion}
	}
	if err := ValidatePackID(strings.TrimSpace(man.ID)); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	if strings.TrimSpace(man.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if _, err := parseCanonicalVersion(man.Version); err != nil {
		return fmt.Errorf("version: %w", err)
	}
	_, err := semver.NewConstraint(strings.TrimSpace(man.Compatibility.ExtensionAPI))
	if err != nil {
		return fmt.Errorf("compatibility.extension_api: %w", err)
	}
	if err := validateCapabilities(man.Compatibility.RequiresCapabilities); err != nil {
		return err
	}
	for id, dep := range man.Dependencies {
		if err := ValidatePackID(id); err != nil {
			return fmt.Errorf("dependency %q: %w", id, err)
		}
		if id == strings.TrimSpace(man.ID) {
			return fmt.Errorf("dependency %q refers to the pack itself", id)
		}
		if strings.TrimSpace(dep.Source) == "" && !IsStockPackID(id) {
			return fmt.Errorf("dependency %q source is required", id)
		}
		if _, err := semver.NewConstraint(strings.TrimSpace(dep.Version)); err != nil {
			return fmt.Errorf("dependency %q version: %w", id, err)
		}
	}
	return nil
}

// ManifestHostCompatible reports whether this host satisfies the declared API range.
func ManifestHostCompatible(man Manifest) bool {
	constraint, err := semver.NewConstraint(strings.TrimSpace(man.Compatibility.ExtensionAPI))
	if err != nil {
		return false
	}
	host, err := semver.StrictNewVersion(ExtensionAPIVersion)
	return err == nil && constraint.Check(host)
}

func parseCanonicalVersion(raw string) (*semver.Version, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("canonical SemVer is required")
	}
	v, err := semver.StrictNewVersion(raw)
	if err != nil {
		return nil, fmt.Errorf("%q is not canonical SemVer: %w", raw, err)
	}
	if v.Original() != raw {
		return nil, fmt.Errorf("%q is not canonical SemVer", raw)
	}
	return v, nil
}

func validateCapabilities(capabilities []string) error {
	seen := map[string]struct{}{}
	for _, capability := range capabilities {
		capability = strings.TrimSpace(capability)
		if _, ok := extensionCapabilities[capability]; !ok {
			return fmt.Errorf("compatibility.requires_capabilities contains unsupported %q", capability)
		}
		if _, duplicate := seen[capability]; duplicate {
			return fmt.Errorf("compatibility.requires_capabilities contains duplicate %q", capability)
		}
		seen[capability] = struct{}{}
	}
	return nil
}

func normalizeManifest(man Manifest) Manifest {
	man.ID = strings.TrimSpace(man.ID)
	man.Name = strings.TrimSpace(man.Name)
	man.Description = strings.TrimSpace(man.Description)
	man.Version = strings.TrimSpace(man.Version)
	man.Compatibility.ExtensionAPI = strings.TrimSpace(man.Compatibility.ExtensionAPI)
	for i := range man.Compatibility.RequiresCapabilities {
		man.Compatibility.RequiresCapabilities[i] = strings.TrimSpace(man.Compatibility.RequiresCapabilities[i])
	}
	sort.Strings(man.Compatibility.RequiresCapabilities)
	for id, dep := range man.Dependencies {
		delete(man.Dependencies, id)
		man.Dependencies[strings.TrimSpace(id)] = DependencyRequest{
			Source: strings.TrimSpace(dep.Source), Version: strings.TrimSpace(dep.Version),
		}
	}
	return man
}

// DependencyIDs returns stable dependency order for resolution and diagnostics.
func (m Manifest) DependencyIDs() []string {
	ids := make([]string, 0, len(m.Dependencies))
	for id := range m.Dependencies {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func dependencyVersionSatisfied(request DependencyRequest, actual string) bool {
	constraint, err := semver.NewConstraint(request.Version)
	if err != nil {
		return false
	}
	version, err := parseCanonicalVersion(actual)
	return err == nil && constraint.Check(version)
}
