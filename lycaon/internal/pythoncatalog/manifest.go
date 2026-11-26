// Package pythoncatalog defines the Portable Python image manifest contract,
// launch package catalog, read-only pip inventory formatting, diagnostics,
// direct tool argument validation, and CLI parsing/routing rules.
package pythoncatalog

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// ImageManifestSchemaVersion identifies the shared PY01/PY02 image manifest JSON contract.
const ImageManifestSchemaVersion = "portable-python.image-manifest/v1"

// ImageManifest is the machine-readable inventory of a built Portable Python image.
// PY02 produces it from the actual built WASI CPython image; PY01 and host tools consume it
// for discovery, read-only pip inventory, CLI dispatch, and diagnostics.
type ImageManifest struct {
	SchemaVersion      string                  `json:"schema_version"`
	ImageID            string                  `json:"image_id"`
	BuiltAt            string                  `json:"built_at"`
	Target             TargetInfo              `json:"target"`
	Interpreter        InterpreterInfo         `json:"interpreter"`
	PackagesRoot       string                  `json:"packages_root"`
	Distributions      []DistributionRecord    `json:"distributions"`
	StdlibCapabilities []StdlibCapabilityGroup `json:"stdlib_capabilities"`
	WasmImports        []WasmImportRecord      `json:"wasm_imports"`
}

// TargetInfo identifies the compiled guest platform and WASI target.
type TargetInfo struct {
	Triple      string `json:"triple"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	WASI        string `json:"wasi"`
	PlatformTag string `json:"platform_tag"`
}

// InterpreterInfo identifies the pinned CPython WASI interpreter and build inputs.
type InterpreterInfo struct {
	Implementation string       `json:"implementation"`
	Version        string       `json:"version"`
	Major          int          `json:"major"`
	Minor          int          `json:"minor"`
	Micro          int          `json:"micro"`
	WasmPath       string       `json:"wasm_path"`
	WasmSHA256     string       `json:"wasm_sha256"`
	StdlibRoot     string       `json:"stdlib_root"`
	StdlibSHA256   string       `json:"stdlib_sha256"`
	BuildInputs    []BuildInput `json:"build_inputs,omitempty"`
	Patches        []string     `json:"patches,omitempty"`
}

// BuildInput records one pinned build input used to produce the image.
type BuildInput struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// DistributionRecord describes one installed Python distribution in the immutable image.
type DistributionRecord struct {
	Name           string                `json:"name"`
	NormalizedName string                `json:"normalized_name"`
	Version        string                `json:"version"`
	Public         bool                  `json:"public"`
	Imports        []string              `json:"imports"`
	Backend        string                `json:"backend,omitempty"`
	Features       []string              `json:"features,omitempty"`
	ConsoleScripts []ConsoleScriptRecord `json:"console_scripts,omitempty"`
	HasPackageData bool                  `json:"has_package_data"`
	DistInfoPath   string                `json:"dist_info_path"`
	License        string                `json:"license"`
	SourceURL      string                `json:"source_url"`
	SourceSHA256   string                `json:"source_sha256"`
	TreeSHA256     string                `json:"tree_sha256"`
	Requires       []string              `json:"requires,omitempty"`
	Workflow       string                `json:"workflow,omitempty"`
}

// ConsoleScriptRecord binds a distribution's console_scripts entry point.
type ConsoleScriptRecord struct {
	Name       string `json:"name"`
	EntryPoint string `json:"entry_point"`
	Module     string `json:"module"`
	Callable   string `json:"callable"`
	Exposed    bool   `json:"exposed"`
}

// StdlibCapabilityGroup documents one qualified standard-library workflow group.
type StdlibCapabilityGroup struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Modules  []string `json:"modules"`
	Workflow string   `json:"workflow"`
}

// WasmImportRecord inventories one host import required by the WASI Python binary.
type WasmImportRecord struct {
	Module             string `json:"module"`
	Name               string `json:"name"`
	Authority          string `json:"authority"`
	FailureDisposition string `json:"failure_disposition"`
}

var pep503Separator = regexp.MustCompile(`[-_.]+`)

// NormalizeDistributionName normalizes a Python distribution name per PEP 503.
func NormalizeDistributionName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	return strings.ToLower(pep503Separator.ReplaceAllString(trimmed, "-"))
}

// ParseImageManifest decodes and validates an ImageManifest JSON document.
func ParseImageManifest(data []byte) (ImageManifest, error) {
	var manifest ImageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ImageManifest{}, fmt.Errorf("decode image manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return ImageManifest{}, err
	}
	return manifest, nil
}

// MarshalImageManifest validates and encodes an ImageManifest as indented JSON.
func MarshalImageManifest(m ImageManifest) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode image manifest: %w", err)
	}
	return append(data, '\n'), nil
}

// Validate checks structural completeness and uniqueness invariants on the manifest.
func (m ImageManifest) Validate() error {
	if m.SchemaVersion != ImageManifestSchemaVersion {
		return fmt.Errorf("unsupported image manifest schema_version %q (want %q)", m.SchemaVersion, ImageManifestSchemaVersion)
	}
	if strings.TrimSpace(m.ImageID) == "" {
		return fmt.Errorf("image manifest missing image_id")
	}
	if strings.TrimSpace(m.BuiltAt) == "" {
		return fmt.Errorf("image manifest missing built_at")
	}
	if strings.TrimSpace(m.Target.Triple) == "" || strings.TrimSpace(m.Target.WASI) == "" {
		return fmt.Errorf("image manifest missing target triple or wasi version")
	}
	if strings.TrimSpace(m.Interpreter.Implementation) == "" || strings.TrimSpace(m.Interpreter.Version) == "" {
		return fmt.Errorf("image manifest missing interpreter implementation or version")
	}
	if strings.TrimSpace(m.PackagesRoot) == "" {
		return fmt.Errorf("image manifest missing packages_root")
	}
	if err := validateDistributions(m.Distributions); err != nil {
		return err
	}
	if err := validateStdlibCapabilities(m.StdlibCapabilities); err != nil {
		return err
	}
	for _, imp := range m.WasmImports {
		if strings.TrimSpace(imp.Module) == "" || strings.TrimSpace(imp.Name) == "" {
			return fmt.Errorf("image manifest wasm_imports entry missing module or name")
		}
	}
	return nil
}

func validateDistributions(dists []DistributionRecord) error {
	if len(dists) == 0 {
		return fmt.Errorf("image manifest has no distributions")
	}
	seenDist := make(map[string]bool, len(dists))
	seenCLI := map[string]bool{}
	for _, d := range dists {
		if strings.TrimSpace(d.Name) == "" || strings.TrimSpace(d.Version) == "" {
			return fmt.Errorf("distribution missing name or version")
		}
		norm := NormalizeDistributionName(d.Name)
		if d.NormalizedName != norm {
			return fmt.Errorf("distribution %q normalized_name %q != %q", d.Name, d.NormalizedName, norm)
		}
		if seenDist[norm] {
			return fmt.Errorf("duplicate distribution %q", norm)
		}
		seenDist[norm] = true
		if len(d.Imports) == 0 {
			return fmt.Errorf("distribution %q has no imports", d.Name)
		}
		if strings.TrimSpace(d.DistInfoPath) == "" {
			return fmt.Errorf("distribution %q missing dist_info_path", d.Name)
		}
		for _, script := range d.ConsoleScripts {
			if strings.TrimSpace(script.Name) == "" || strings.TrimSpace(script.EntryPoint) == "" {
				return fmt.Errorf("distribution %q has incomplete console script", d.Name)
			}
			if script.Exposed {
				if seenCLI[script.Name] {
					return fmt.Errorf("duplicate exposed console script %q", script.Name)
				}
				seenCLI[script.Name] = true
			}
		}
	}
	return nil
}

func validateStdlibCapabilities(groups []StdlibCapabilityGroup) error {
	seen := make(map[string]bool, len(groups))
	for _, g := range groups {
		if strings.TrimSpace(g.ID) == "" || strings.TrimSpace(g.Title) == "" || len(g.Modules) == 0 {
			return fmt.Errorf("invalid stdlib capability group %q", g.ID)
		}
		if seen[g.ID] {
			return fmt.Errorf("duplicate stdlib capability group %q", g.ID)
		}
		seen[g.ID] = true
	}
	return nil
}

// FindDistribution looks up a distribution by PEP 503 normalized name.
func (m ImageManifest) FindDistribution(name string) (DistributionRecord, bool) {
	norm := NormalizeDistributionName(name)
	if norm == "" {
		return DistributionRecord{}, false
	}
	for _, d := range m.Distributions {
		if d.NormalizedName == norm {
			return d, true
		}
	}
	return DistributionRecord{}, false
}

// FindByImport looks up the distribution providing the exact Python import name.
func (m ImageManifest) FindByImport(importName string) (DistributionRecord, bool) {
	trimmed := strings.TrimSpace(importName)
	if trimmed == "" {
		return DistributionRecord{}, false
	}
	for _, d := range m.Distributions {
		if slices.Contains(d.Imports, trimmed) {
			return d, true
		}
	}
	return DistributionRecord{}, false
}

// FindConsoleScript looks up an advertised (exposed) console script by command name.
func (m ImageManifest) FindConsoleScript(cliName string) (DistributionRecord, ConsoleScriptRecord, bool) {
	trimmed := strings.TrimSpace(cliName)
	if trimmed == "" {
		return DistributionRecord{}, ConsoleScriptRecord{}, false
	}
	for _, d := range m.Distributions {
		for _, s := range d.ConsoleScripts {
			if s.Exposed && s.Name == trimmed {
				return d, s, true
			}
		}
	}
	return DistributionRecord{}, ConsoleScriptRecord{}, false
}

// PublicDistributions returns the distributions marked public in manifest order.
func (m ImageManifest) PublicDistributions() []DistributionRecord {
	out := make([]DistributionRecord, 0, len(m.Distributions))
	for _, d := range m.Distributions {
		if d.Public {
			out = append(out, d)
		}
	}
	return out
}

// AdvertisedConsoleScripts returns every exposed console script in manifest order.
func (m ImageManifest) AdvertisedConsoleScripts() []ConsoleScriptRecord {
	var out []ConsoleScriptRecord
	for _, d := range m.Distributions {
		for _, s := range d.ConsoleScripts {
			if s.Exposed {
				out = append(out, s)
			}
		}
	}
	return out
}
