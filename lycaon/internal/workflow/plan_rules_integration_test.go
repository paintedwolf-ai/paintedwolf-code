//go:build integration

package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/rules/rulestest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSpecPostureBlocksStateWhenStubInvalid(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "register rule conditions", err)
	}
	engine := rulestest.BundledEngine(t, reg)
	out, err := engine.Evaluate(context.Background(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, ToolName: "state_create"}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if out.Allowed {
		t.Fatal("expected deny for state_create with invalid stub")
	}
	if out.PhaseRequired != "1" {
		t.Fatalf("phase_required = %q want 1", out.PhaseRequired)
	}
}

func TestSpecPostureBlocksDelegateWithStubInvalid(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "register rule conditions", err)
	}
	engine := rulestest.BundledEngine(t, reg)
	out, err := engine.Evaluate(context.Background(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, ToolName: "plan_from_blueprint", PlanContent: ""}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if out.Allowed {
		t.Fatal("expected deny for plan_from_blueprint with invalid stub")
	}
	if out.Code != "SPEC_POSTURE_DELEGATION_FORBIDDEN" {
		t.Fatalf("code = %q", out.Code)
	}
	if out.PhaseRequired == "" {
		t.Fatal("expected phase_required on deny")
	}
}

func TestBuildPostureAllowsImplementerTask(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDepsWithEvidence())
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "register rule conditions", err)
	}
	engine := rulestest.BundledEngine(t, reg)
	out, err := engine.Evaluate(context.Background(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureBuild, ToolName: "task", ToolArgs: map[string]any{"agent_type": "implementer"}, AllowedAgents: []string{"implementer"}}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if !out.Allowed {
		t.Fatalf("expected allow, code=%s msg=%s", out.Code, out.Message)
	}
}
