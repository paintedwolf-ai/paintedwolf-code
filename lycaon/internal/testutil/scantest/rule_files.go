package scantest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	severityError   = "ERROR"
	severityWarning = "WARNING"
)

var idPrefixPattern = regexp.MustCompile(`^lycaon\.[a-z0-9]+[a-z0-9.-]*$`)

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

// LoadRuleFile reads a scanner rule YAML file.
func LoadRuleFile(path string) (*RuleFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rf RuleFile
	// Contracts read only the structural fields they validate.
	if err := yaml.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &rf, nil
}

func validateRule(r Rule) error {
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
	case severityError, severityWarning:
	default:
		return fmt.Errorf("rule %q severity must be ERROR or WARNING, got %q", r.ID, r.Severity)
	}
	return nil
}

// ValidateRuleFile checks structural rule contracts.
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
		if err := validateRule(r); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if _, dup := seen[r.ID]; dup {
			return fmt.Errorf("%s: duplicate rule id %q", path, r.ID)
		}
		seen[r.ID] = struct{}{}
	}
	return nil
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
		if strings.HasSuffix(path, ".test.yaml") || strings.HasSuffix(path, ".test.yml") || (filepath.Ext(path) != ".yaml" && filepath.Ext(path) != ".yml") {
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
