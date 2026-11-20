package rules

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOrderedRulesDenyDelegationInSpecPosture(t *testing.T) {
	cfg := loadSpecRules(t)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	if err := RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "RegisterRuleConditions failed", err)
	}
	outcome, err := evaluateOrderedRules(reg, cfg.Rules, EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, ToolName: "plan_from_blueprint"}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if outcome.Allowed {
		t.Fatal("expected plan_from_blueprint denied in plan mode")
	}
	if outcome.Code != "SPEC_POSTURE_DELEGATION_FORBIDDEN" {
		t.Fatalf("code = %q", outcome.Code)
	}
}
