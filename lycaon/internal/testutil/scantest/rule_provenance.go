package scantest

import (
	"fmt"

	"github.com/lycaon/lycaon/config"
)

var rulesProvenance = config.ScannersDir.Join("rules-provenance.yaml")

// VendorRulesProvenance pins one vendored rule catalog.
type VendorRulesProvenance struct {
	ID       string `yaml:"id"`
	Upstream string `yaml:"upstream"`
	Ref      string `yaml:"ref"`
	Commit   string `yaml:"commit"`
	// TreeSHA256 is the pinned commit's bytes before any patch.
	TreeSHA256 string `yaml:"tree_sha256"`
	// PatchSHA256 and VendoredSHA256 are set only for a catalog carrying local
	// fixes in rules/patches/<id>/; what ships is then VendoredSHA256.
	PatchSHA256    string   `yaml:"patch_sha256,omitempty"`
	VendoredSHA256 string   `yaml:"vendored_sha256,omitempty"`
	License        string   `yaml:"license"`
	Include        []string `yaml:"include"`
	Paths          []string `yaml:"paths"`
}

// RulesProvenanceConfig is the bundled rules-provenance ledger.
type RulesProvenanceConfig struct {
	Vendors []VendorRulesProvenance `yaml:"vendors"`
	Lycaon  LycaonProvenanceBlock   `yaml:"lycaon"`
}

// ProvenanceFileEntry is one file row under rules-provenance.yaml lycaon.files.
type ProvenanceFileEntry struct {
	Path       string `yaml:"path"`
	Test       string `yaml:"test,omitempty"`
	ParserMode string `yaml:"parser_mode,omitempty"`
}

// LycaonProvenanceBlock is the in-repo lycaon section of rules-provenance.yaml.
type LycaonProvenanceBlock struct {
	License    string                `yaml:"license"`
	Origin     string                `yaml:"origin"`
	Authorship string                `yaml:"authorship"`
	Files      []ProvenanceFileEntry `yaml:"files"`
}

// LycaonProvenanceOriginBundledInRepo marks first-party gap-fill rules shipped in-repo (CC-BY-4.0).
const LycaonProvenanceOriginBundledInRepo = "bundled-in-repo"

// LoadRulesProvenance returns the bundled provenance ledger for the shipped rules.
func LoadRulesProvenance() (*RulesProvenanceConfig, error) {
	data, err := config.Read(rulesProvenance)
	if err != nil {
		return nil, err
	}
	var cfg RulesProvenanceConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse rules-provenance: %w", err)
	}
	return &cfg, nil
}
