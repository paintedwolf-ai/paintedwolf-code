package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/rules"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestSpecRulesReferenceKnownDenyCodes(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	assertRulesDenyCodesKnown(t, root, "spec.yaml")
}

func assertRulesDenyCodesKnown(t *testing.T, root, rulesFile string) {
	t.Helper()
	rulesCfg, err := rules.LoadRulesConfig(config.PostureRulesDir.Join(rulesFile))
	contractcheck.FailErr(t, "load rules config YAML", err)
	hintCfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	for _, rule := range rulesCfg.Rules {
		deny, ok := rule.Then["deny"].(map[string]any)
		if !ok {
			continue
		}
		code, _ := deny["code"].(string)
		if code == "" {
			continue
		}
		if _, known := hintCfg.HintCodes[code]; known {
			continue
		}
		if !isAllowedDenyCode(code) {
			t.Fatalf("%s rule %q references unknown deny code %q (not in hint registry and not posture-namespaced)", rulesFile, rule.ID, code)
		}
	}
}

func TestSpecPostureHintsReferenceSpecRuleCodes(t *testing.T) {
	t.Parallel()
	hintCfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	for _, code := range []string{
		"SPEC_POSTURE_UNRESOLVED",
		"SPEC_POSTURE_STATE_FORBIDDEN",
		"SPEC_POSTURE_DELEGATION_FORBIDDEN",
		"SPEC_POSTURE_STUB_REQUIRED",
	} {
		if _, ok := hintCfg.HintCodes[code]; !ok {
			t.Fatalf("missing spec posture hint %q", code)
		}
	}
}

// hintEntryHasProse is true when YAML carries prose fields plus a branch instruction.
func hintEntryHasProse(entry guidance.HintEntry) bool {
	if strings.TrimSpace(entry.Instead) == "" {
		return false
	}
	if strings.TrimSpace(entry.What) != "" {
		return true
	}
	return strings.TrimSpace(entry.Message) != ""
}
