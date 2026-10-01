package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/pkg/api"
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
					if !session.ValidSessionPosture(val) {
						t.Errorf("%s rule %q: invalid posture_is %q", e.Name(), rule.ID, val)
					}
				}
			}
		}
	}
}

func TestRuleEnginePostureBehaviorContract(t *testing.T) {
	t.Parallel()
	postures, err := session.LoadPostureRegistry()
	contractcheck.FailErr(t, "session.LoadPostureRegistry failed", err)
	packs, err := rules.LoadBundledRules()
	contractcheck.FailErr(t, "rules.LoadBundledRules failed", err)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	contractcheck.FailErr(t, "register rule conditions", rules.RegisterRuleConditions(reg))
	engine, err := rules.NewPostureRuleEngine(postures, packs, reg)
	contractcheck.FailErr(t, "rules.NewPostureRuleEngine failed", err)
	ctx := context.Background()

	deny, err := engine.Evaluate(ctx, rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, ToolName: "delegate_dispatch"}})
	contractcheck.FailErr(t, "engine.Evaluate failed", err)
	if deny.Allowed {
		t.Fatal("expected delegate_dispatch denied in spec posture")
	}
	if deny.Code != "SPEC_POSTURE_DELEGATION_FORBIDDEN" {
		t.Fatalf("code = %q", deny.Code)
	}

	allow, err := engine.Evaluate(ctx, rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureBuild, ToolName: "delegate_dispatch"}})
	contractcheck.FailErr(t, "engine.Evaluate failed", err)
	if !allow.Allowed {
		t.Fatalf("expected delegate_dispatch allowed in build posture, got %q", allow.Code)
	}
}
