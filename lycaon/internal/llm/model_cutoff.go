package llm

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
)

// Model-family knowledge cutoffs define training data recency for prompt assembly.

// CutoffRule maps model-id substrings to a knowledge-cutoff month.
type CutoffRule struct {
	// Match is a list of case-insensitive substrings tested against the model
	// id; any hit selects the rule.
	Match []string `yaml:"match"`
	// Cutoff is the approximate end of the family's training data, "YYYY-MM".
	Cutoff string `yaml:"cutoff"`
}

var modelCutoffRules atomic.Pointer[[]CutoffRule]

// SetModelCutoffRules installs the active rule set (user rules first, then
// bundled — first match wins). ProviderCatalog.Reload is the production caller.
func SetModelCutoffRules(rules []CutoffRule) {
	own := append([]CutoffRule(nil), rules...)
	modelCutoffRules.Store(&own)
}

func activeModelCutoffRules() []CutoffRule {
	if p := modelCutoffRules.Load(); p != nil {
		return *p
	}
	return nil
}

const cutoffMonthLayout = "2006-01"

// ValidateCutoffRules rejects rules with an empty match list or a cutoff that
// is not a YYYY-MM month — a bad rule set fails the catalog load rather than
// silently rendering junk into prompts.
func ValidateCutoffRules(rules []CutoffRule) error {
	for i, rule := range rules {
		if len(rule.Match) == 0 {
			return fmt.Errorf("model_cutoff[%d]: empty match list", i)
		}
		for _, m := range rule.Match {
			if strings.TrimSpace(m) == "" {
				return fmt.Errorf("model_cutoff[%d]: blank match pattern", i)
			}
		}
		if _, err := time.Parse(cutoffMonthLayout, strings.TrimSpace(rule.Cutoff)); err != nil {
			return fmt.Errorf("model_cutoff[%d]: cutoff %q is not YYYY-MM", i, rule.Cutoff)
		}
	}
	return nil
}

// ResolveModelCutoff returns the knowledge-cutoff month ("YYYY-MM") for a
// model id, or "" when no family rule matches.
func ResolveModelCutoff(model string) string {
	for _, rule := range activeModelCutoffRules() {
		if modelinfo.MatchFirstSubstring(model, rule.Match) {
			return strings.TrimSpace(rule.Cutoff)
		}
	}
	return ""
}
