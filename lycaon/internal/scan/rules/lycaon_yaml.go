package rules

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/scan/opengrep"
	"gopkg.in/yaml.v3"
)

const (
	SeverityError   = "ERROR"
	SeverityWarning = "WARNING"
)

// rulesProvenance is the vendored-rule provenance ledger. It has no named
// constant in config/paths.go because nothing outside this package addresses it.
var rulesProvenance = config.ScannersDir.Join("rules-provenance.yaml")

var (
	idPrefixPattern = regexp.MustCompile(`^lycaon\.[a-z0-9]+[a-z0-9.-]*$`)
)

// RuleFile is the bundled rule YAML envelope.
type RuleFile struct {
	Rules []Rule `yaml:"rules"`
}

// Rule is one bundled gate rule.
type Rule struct {
	ID        string        `yaml:"id"`
	Languages []string      `yaml:"languages"`
	Severity  string        `yaml:"severity"`
	Message   string        `yaml:"message,omitempty"`
	Metadata  *RuleMetadata `yaml:"metadata,omitempty"`
	Paths     *RulePaths    `yaml:"paths,omitempty"`
}

// RulePaths filters files for a rule.
type RulePaths struct {
	Include []string `yaml:"include,omitempty"`
	Exclude []string `yaml:"exclude,omitempty"`
}

// RuleMetadata holds optional rule metadata (CWE and parser mode).
type RuleMetadata struct {
	Purpose    string `yaml:"purpose,omitempty"`
	CWE        string `yaml:"cwe,omitempty"`
	ParserMode string `yaml:"parser_mode,omitempty"`
	Notes      string `yaml:"notes,omitempty"`
}

type OpengrepGatesConfig struct {
	Analysis opengrep.Analysis `yaml:"analysis"`
	Vendor   []string          `yaml:"vendor"`
	Lycaon   []string          `yaml:"lycaon"`
}

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

// LoadRuleFile reads and parses a lycaon rule YAML file.
func LoadRuleFile(path string) (*RuleFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rf RuleFile
	// Decode only the fields used by rule execution.
	if err := yaml.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &rf, nil
}

// ValidateRule checks one rule entry against lycaon gate policy.
func ValidateRule(r Rule) error {
	if strings.TrimSpace(r.ID) == "" {
		return fmt.Errorf("rule missing id")
	}
	if !idPrefixPattern.MatchString(r.ID) {
		return fmt.Errorf("rule id %q must match lycaon.{lang}.{slug}", r.ID)
	}
	if len(r.Languages) == 0 {
		return fmt.Errorf("rule %q missing languages", r.ID)
	}
	if r.Metadata != nil && r.Metadata.Purpose == "coverage" {
		if r.Severity != "INFO" || r.Paths == nil || len(r.Paths.Include) == 0 {
			return fmt.Errorf("coverage observation %q must be INFO and path scoped", r.ID)
		}
		return nil
	}
	switch strings.ToUpper(strings.TrimSpace(r.Severity)) {
	case SeverityError, SeverityWarning:
	default:
		return fmt.Errorf("rule %q severity must be ERROR or WARNING, got %q", r.ID, r.Severity)
	}
	return nil
}

// ValidateRuleFile validates all rules in a lycaon gate rule file.
func ValidateRuleFile(path string) error {
	rf, err := LoadRuleFile(path)
	if err != nil {
		return err
	}
	if len(rf.Rules) == 0 {
		return fmt.Errorf("%s: no rules", path)
	}
	seen := make(map[string]struct{}, len(rf.Rules))
	for _, r := range rf.Rules {
		if err := ValidateRule(r); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if _, dup := seen[r.ID]; dup {
			return fmt.Errorf("%s: duplicate rule id %q", path, r.ID)
		}
		seen[r.ID] = struct{}{}
	}
	return nil
}

func LoadOpengrepGates() (*OpengrepGatesConfig, error) {
	data, err := config.Read(config.OpengrepGates)
	if err != nil {
		return nil, err
	}
	var cfg OpengrepGatesConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse opengrep-gates: %w", err)
	}
	if err := cfg.Analysis.Validate(); err != nil {
		return nil, fmt.Errorf("opengrep-gates analysis: %w", err)
	}
	return &cfg, nil
}

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

// ResolveModulePath joins the module root with a repository-relative path.
func ResolveModulePath(lycaonRoot, rel string) string {
	return filepath.Join(lycaonRoot, filepath.FromSlash(rel))
}

// CollectRuleIDsFromDir returns rule IDs from YAML gate files.
func CollectRuleIDsFromDir(dir string) (map[string]string, error) {
	ids := make(map[string]string)
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !isGateRuleYAML(path) {
			return nil
		}
		rf, err := LoadRuleFile(path)
		if err != nil {
			return err
		}
		for _, r := range rf.Rules {
			if prev, dup := ids[r.ID]; dup {
				return fmt.Errorf("duplicate rule id %q in %s and %s", r.ID, prev, path)
			}
			ids[r.ID] = path
		}
		return nil
	})
	return ids, err
}

// ActiveLycaonGatePaths returns configured first-party rule paths.
func ActiveLycaonGatePaths(cfg *OpengrepGatesConfig) []string {
	if cfg == nil {
		return nil
	}
	out := make([]string, 0, len(cfg.Lycaon))
	for _, p := range cfg.Lycaon {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ActiveVendorGatePaths returns configured vendor rule paths.
func ActiveVendorGatePaths(cfg *OpengrepGatesConfig) []string {
	if cfg == nil {
		return nil
	}
	out := make([]string, 0, len(cfg.Vendor))
	for _, p := range cfg.Vendor {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func isGateRuleYAML(path string) bool {
	if strings.HasSuffix(path, ".test.yaml") || strings.HasSuffix(path, ".test.yml") {
		return false
	}
	return strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")
}
