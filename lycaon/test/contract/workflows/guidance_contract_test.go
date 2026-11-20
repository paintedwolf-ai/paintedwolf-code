package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/rules"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

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

// Every deny code spec.yaml emits has a hint entry; spec codes take no namespace escape.
func TestSpecPostureHintsReferenceSpecRuleCodes(t *testing.T) {
	t.Parallel()
	hintCfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	rulesCfg, err := rules.LoadRulesConfig(config.PostureRulesDir.Join("spec.yaml"))
	contractcheck.FailErr(t, "load spec rules", err)
	denies := 0
	for _, rule := range rulesCfg.Rules {
		deny, ok := rule.Then["deny"].(map[string]any)
		if !ok {
			continue
		}
		code, _ := deny["code"].(string)
		if code == "" {
			continue
		}
		denies++
		if _, ok := hintCfg.HintCodes[code]; !ok {
			t.Errorf("spec rule %q denies with %q, which has no hint entry", rule.ID, code)
		}
	}
	if denies == 0 {
		t.Fatal("spec.yaml carries no deny codes")
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
