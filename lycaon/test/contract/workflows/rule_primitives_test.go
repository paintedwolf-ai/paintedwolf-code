package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func knownRuleWhenKeys() map[string]struct{} {
	out := make(map[string]struct{}, len(rules.RuleWhenKeys()))
	for _, k := range rules.RuleWhenKeys() {
		out[k] = struct{}{}
	}
	return out
}

func ruleConditionRegistry(t *testing.T) *conditions.ConditionRegistry {
	t.Helper()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	contractcheck.FailErr(t, "register rule conditions", rules.RegisterRuleConditions(reg))
	return reg
}

func TestRuleYAMLWhenKeysAreImplemented(t *testing.T) {
	reg := ruleConditionRegistry(t)
	fixedKeys := knownRuleWhenKeys()
	entries, err := config.List(config.PostureRulesDir)
	contractcheck.FailErr(t, "read directory entries", err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		cfg, err := rules.LoadRulesConfig(config.PostureRulesDir.Join(e.Name()))
		contractcheck.FailErr(t, "load rules config YAML", err)
		for _, rule := range cfg.Rules {
			for key, raw := range rule.When {
				_, fixed := fixedKeys[key]
				if !fixed && !reg.Has(key) {
					t.Errorf("%s rule %q: unknown when key %q", e.Name(), rule.ID, key)
				}
				if key == "posture_is" {
					val, _ := raw.(string)
					if !sessionposture.ValidSessionPosture(val) {
						t.Errorf("%s rule %q: invalid posture_is %q", e.Name(), rule.ID, val)
					}
				}
			}
		}
	}
}
