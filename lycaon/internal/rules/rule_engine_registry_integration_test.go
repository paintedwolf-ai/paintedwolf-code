//go:build integration

package rules

import (
	"context"
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
	ctx := context.Background()
	for _, cfg := range configs {
		engine := NewSimpleEngine(cfg, reg)
		for _, rule := range cfg.Rules {
			whenStr, err := CanonicalWhenString(rule.When)
			if err != nil {
				t.Fatalf("pack rule %q: %v", rule.ID, err)
			}
			ok, err := MatchWhenExpr(reg, whenStr, EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, ToolName: "read"}})
			if err != nil {
				t.Fatalf("pack rule %q when %q: %v", rule.ID, whenStr, err)
			}
			_ = ok
			out, err := engine.Evaluate(ctx, EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, ToolName: "delegate_dispatch"}})
			if err != nil {
				t.Fatalf("pack evaluate %q: %v", rule.ID, err)
			}
			_ = out
		}
	}
}

func TestSpecRulesDenyDelegationViaRegistryEngine(t *testing.T) {
	cfg := loadSpecRules(t)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	if err := RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "RegisterRuleConditions failed", err)
	}
	engine := NewSimpleEngine(cfg, reg)
	out, err := engine.Evaluate(context.Background(), EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, ToolName: "delegate_dispatch"}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if out.Allowed {
		t.Fatal("expected deny")
	}
	if out.Code != "SPEC_POSTURE_DELEGATION_FORBIDDEN" {
		t.Fatalf("code = %q", out.Code)
	}
}
