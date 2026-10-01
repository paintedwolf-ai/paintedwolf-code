package rules

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSimpleEngineDeniesDelegationInPlanMode(t *testing.T) {
	cfg := loadSpecRules(t)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	if err := RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "RegisterRuleConditions failed", err)
	}
	engine := NewSimpleEngine(cfg, reg)
	outcome, err := engine.Evaluate(context.Background(), EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, ToolName: "delegate_dispatch"}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if outcome.Allowed {
		t.Fatal("expected delegate_dispatch denied in plan mode")
	}
	if outcome.Code != "SPEC_POSTURE_DELEGATION_FORBIDDEN" {
		t.Fatalf("code = %q", outcome.Code)
	}
}
