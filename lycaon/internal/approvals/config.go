package approvals

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/approvalregistry"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"gopkg.in/yaml.v3"
)

// Config is the merged on-disk approval_explanations registry.
type Config struct {
	Explanations map[string]ExplanationEntry `yaml:"approval_explanations"`
}

// ExplanationEntry is one reviewed approval explanation template set.
type ExplanationEntry struct {
	Tool        string          `yaml:"tool,omitempty"`
	TierHint    string          `yaml:"tier_hint,omitempty"`
	WhatChanges string          `yaml:"what_changes,omitempty"`
	WhoAffected string          `yaml:"who_affected,omitempty"`
	IfWrong     string          `yaml:"if_wrong,omitempty"`
	AllowLine   string          `yaml:"allow_line,omitempty"`
	Scenarios   []ScenarioEntry `yaml:"scenarios,omitempty"`
}

// ScenarioEntry is a parametrized contract-test render sample.
type ScenarioEntry struct {
	ID             string         `yaml:"id"`
	Vars           map[string]any `yaml:"vars,omitempty"`
	ExpectContains []string       `yaml:"expect_contains,omitempty"`
}

// ExplanationCopy is rendered host copy for a tool approval checkpoint.
type ExplanationCopy struct {
	What      string `json:"what"`
	Who       string `json:"who"`
	IfWrong   string `json:"if_wrong"`
	AllowLine string `json:"allow_line"`
}

// ExplanationResult is ExplainAction output including fallback metadata.
type ExplanationResult struct {
	Key          string
	Copy         ExplanationCopy
	UsedFallback bool
}

const explainFileHeader = "# Enriched by ./task codegen:approval-explanations\n\n"

// WriteExplainYAML writes one registry entry to path.
func WriteExplainYAML(path, key string, entry ExplanationEntry) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("explanation key required")
	}
	body, err := yaml.Marshal(&Config{Explanations: map[string]ExplanationEntry{key: entry}})
	if err != nil {
		return err
	}
	header := "# " + key + " — approval explanation registry entry.\n" + explainFileHeader
	return approvalregistry.WriteFile(path, []byte(header+string(body)))
}

// ValidateConfig enforces schema + scenarios for every entry.
func ValidateConfig(cfg *Config) error {
	if cfg == nil || len(cfg.Explanations) == 0 {
		return fmt.Errorf("approval_explanations required")
	}
	var errs []string
	for key, entry := range cfg.Explanations {
		if err := validateEntry(key, entry); err != nil {
			errs = append(errs, err.Error())
		}
	}
	sort.Strings(errs)
	if len(errs) > 0 {
		return fmt.Errorf("approval-explanations:\n%s", strings.Join(errs, "\n"))
	}
	return nil
}

func validateEntry(key string, entry ExplanationEntry) error {
	if strings.TrimSpace(entry.WhatChanges) == "" {
		return fmt.Errorf("%s: what_changes required", key)
	}
	if strings.TrimSpace(entry.IfWrong) == "" {
		return fmt.Errorf("%s: if_wrong required", key)
	}
	if strings.TrimSpace(entry.WhoAffected) == "" {
		return fmt.Errorf("%s: who_affected required", key)
	}
	if strings.TrimSpace(entry.AllowLine) == "" {
		return fmt.Errorf("%s: allow_line required", key)
	}
	if len(entry.Scenarios) == 0 {
		return fmt.Errorf("%s: scenarios required (>=1)", key)
	}
	for _, sc := range entry.Scenarios {
		if strings.TrimSpace(sc.ID) == "" {
			return fmt.Errorf("%s: scenario id required", key)
		}
		if len(sc.ExpectContains) < 2 {
			return fmt.Errorf("%s: scenario %q expect_contains required (>=2)", key, sc.ID)
		}
	}
	for _, field := range []struct {
		name, val string
	}{
		{"what_changes", entry.WhatChanges},
		{"who_affected", entry.WhoAffected},
		{"if_wrong", entry.IfWrong},
		{"allow_line", entry.AllowLine},
	} {
		if err := validatePongoField(key, field.name, field.val); err != nil {
			return err
		}
	}
	for _, scenario := range entry.Scenarios {
		for _, field := range []struct{ name, val string }{
			{"what_changes", entry.WhatChanges},
			{"who_affected", entry.WhoAffected},
			{"if_wrong", entry.IfWrong},
			{"allow_line", entry.AllowLine},
		} {
			if _, err := renderTemplateChecked(field.val, ScenarioVars(scenario)); err != nil {
				return fmt.Errorf("%s: scenario %q %s pongo render: %w", key, scenario.ID, field.name, err)
			}
		}
	}
	return nil
}

func validatePongoField(key, name, val string) error {
	val = strings.TrimSpace(val)
	if val == "" || !strings.Contains(val, "{{") && !strings.Contains(val, "{%") {
		return nil
	}
	if _, err := pongoplain.Compile(val); err != nil {
		return fmt.Errorf("%s: %s pongo parse: %w", key, name, err)
	}
	return nil
}
