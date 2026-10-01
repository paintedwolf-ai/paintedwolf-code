//go:build integration

package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSpecPostureBlocksStateWhenStubInvalid(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "register rule conditions", err)
	}
	engine := rules.NewSimpleEngine(loadSpecRules(t), reg)
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
	engine := rules.NewSimpleEngine(loadSpecRules(t), reg)
	out, err := engine.Evaluate(context.Background(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, ToolName: "delegate_dispatch", PlanContent: ""}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if out.Allowed {
		t.Fatal("expected deny for delegate_dispatch with invalid stub")
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
	buildCfg, err := rules.LoadRulesConfig(config.PostureRulesDir.Join("build.yaml"))
	testutil.FailErr(t, "load rules config YAML", err)
	engine := rules.NewSimpleEngine(buildCfg, reg)
	out, err := engine.Evaluate(context.Background(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureBuild, ToolName: "task", ToolArgs: map[string]any{"agent_type": "implementer"}, AllowedAgents: []string{"implementer"}}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if !out.Allowed {
		t.Fatalf("expected allow, code=%s msg=%s", out.Code, out.Message)
	}
}

func loadSpecRules(t *testing.T) *rules.RulesConfig {
	t.Helper()
	cfg, err := rules.LoadRulesConfig(config.PostureRulesDir.Join("spec.yaml"))
	testutil.FailErr(t, "load rules config YAML", err)
	return cfg
}
