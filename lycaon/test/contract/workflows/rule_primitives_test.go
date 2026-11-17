package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestRuleEnginePostureBehaviorContract(t *testing.T) {
	t.Parallel()
	postures, err := profiles.LoadPostureRegistry()
	contractcheck.FailErr(t, "profiles.LoadPostureRegistry failed", err)
	packs, err := rules.LoadBundledRules()
	contractcheck.FailErr(t, "rules.LoadBundledRules failed", err)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	contractcheck.FailErr(t, "register rule conditions", rules.RegisterRuleConditions(reg))
	engine, err := rules.NewPostureRuleEngine(postures, packs, reg)
	contractcheck.FailErr(t, "rules.NewPostureRuleEngine failed", err)
	ctx := context.Background()

	deny, err := engine.Evaluate(ctx, rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, ToolName: "plan_from_blueprint"}})
	contractcheck.FailErr(t, "engine.Evaluate failed", err)
	if deny.Allowed {
		t.Fatal("expected plan_from_blueprint denied in spec posture")
	}
	if deny.Code != "SPEC_POSTURE_DELEGATION_FORBIDDEN" {
		t.Fatalf("code = %q", deny.Code)
	}

	allow, err := engine.Evaluate(ctx, rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureBuild, ToolName: "plan_from_blueprint"}})
	contractcheck.FailErr(t, "engine.Evaluate failed", err)
	if !allow.Allowed {
		t.Fatalf("expected plan_from_blueprint allowed in build posture, got %q", allow.Code)
	}
}
