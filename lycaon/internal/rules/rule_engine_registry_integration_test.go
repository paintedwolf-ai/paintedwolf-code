//go:build integration

package rules

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBundledRulesResolveViaRegistry(t *testing.T) {
	configs, err := LoadBundledRuleConfigs()
	testutil.FailErr(t, "LoadBundledRuleConfigs failed", err)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	if err := RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "RegisterRuleConditions failed", err)
	}
	read := EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, ToolName: "read"}}
	delegate := EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, ToolName: "plan_from_blueprint"}}
	for _, cfg := range configs {
		for _, rule := range cfg.Rules {
			if _, err := MatchWhen(reg, rule.When, read); err != nil {
				t.Errorf("pack rule %q: %v", rule.ID, err)
			}
		}
		_, err := evaluateOrderedRules(reg, cfg.Rules, delegate)
		testutil.FailErr(t, "evaluate bundled rule file", err)
	}
}

func TestSpecRulesDenyDelegationViaRegistryEngine(t *testing.T) {
	cfg := loadSpecRules(t)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	if err := RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "RegisterRuleConditions failed", err)
	}
	out, err := evaluateOrderedRules(reg, cfg.Rules, EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, ToolName: "plan_from_blueprint"}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if out.Allowed {
		t.Fatal("expected deny")
	}
	if out.Code != "SPEC_POSTURE_DELEGATION_FORBIDDEN" {
		t.Fatalf("code = %q", out.Code)
	}
}
