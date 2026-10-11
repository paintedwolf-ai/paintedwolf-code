package rules_test

import (
	"context"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPostureRuleEngineSpecDeniesDelegation(t *testing.T) {
	engine := newBundledPostureEngine(t)
	out, err := engine.Evaluate(context.Background(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, ToolName: "delegate_dispatch"}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if out.Allowed {
		t.Fatal("expected deny")
	}
	if out.Code != "SPEC_POSTURE_DELEGATION_FORBIDDEN" {
		t.Fatalf("code = %q", out.Code)
	}
}

func TestPostureRuleEngineBuildAllowsDelegation(t *testing.T) {
	engine := newBundledPostureEngine(t)
	out, err := engine.Evaluate(context.Background(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureBuild, ToolName: "delegate_dispatch"}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if !out.Allowed {
		t.Fatalf("expected allow, got deny %q", out.Code)
	}
}

func TestPostureRuleEngineBuildDeniesDisallowedAgent(t *testing.T) {
	engine := newBundledPostureEngine(t)
	out, err := engine.Evaluate(context.Background(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureBuild, ToolName: "task", ToolArgs: map[string]any{"agent_type": "not-in-allowlist"}, AllowedAgents: []string{"implementer", "plan-writer"}}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if out.Allowed {
		t.Fatal("expected deny")
	}
	if out.Code != "DISALLOWED_AGENT" {
		t.Fatalf("code = %q", out.Code)
	}
}

func TestPostureRuleEngineOrchestrateDeniesPlanWriter(t *testing.T) {
	engine := newBundledPostureEngine(t)
	out, err := engine.Evaluate(context.Background(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureOrchestrate, ToolName: "task", ToolArgs: map[string]any{"agent_type": "plan-writer"}}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if out.Allowed {
		t.Fatal("expected deny")
	}
	if out.Code != "ORCHESTRATE_POSTURE_NO_PLAN_WRITER" {
		t.Fatalf("code = %q", out.Code)
	}
}

func TestPostureRuleEngineVetDeniesDelegation(t *testing.T) {
	engine := newBundledPostureEngine(t)
	out, err := engine.Evaluate(context.Background(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureVet, ToolName: "delegate_dispatch"}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if out.Allowed {
		t.Fatal("expected deny")
	}
	if out.Code != "VET_POSTURE_DELEGATION_FORBIDDEN" {
		t.Fatalf("code = %q", out.Code)
	}
}

// A vet turn resolves to an observe_ surface that offers no write, so the rule
// plane does not answer writes.
func TestPostureRuleEngineVetLeavesWriteToTheSurface(t *testing.T) {
	engine := newBundledPostureEngine(t)
	out, err := engine.Evaluate(context.Background(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureVet, ToolName: "write"}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if !out.Allowed {
		t.Fatalf("vet write denied by a rule (code %q); the surface owns that boundary", out.Code)
	}
}

func TestPostureRuleEngineUnknownPostureFailClosed(t *testing.T) {
	engine := newBundledPostureEngine(t)
	out, err := engine.Evaluate(context.Background(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: "", ToolName: "delegate_dispatch"}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if out.Allowed {
		t.Fatal("expected deny for unresolved posture")
	}
	if out.Code != "SPEC_POSTURE_UNRESOLVED" {
		t.Fatalf("code = %q", out.Code)
	}
}

func TestPostureRuleEngineUsesBuildRulesNotSpec(t *testing.T) {
	engine := newBundledPostureEngine(t)
	out, err := engine.Evaluate(context.Background(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureBuild, ToolName: "write"}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if !out.Allowed {
		t.Fatalf("build posture should not apply vet write rule, got %q", out.Code)
	}
}

func TestPostureRuleEngineAppendsManifestRules(t *testing.T) {
	engine := newBundledPostureEngine(t)
	out, err := engine.Evaluate(context.Background(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureBuild, ToolName: "delegate_dispatch"}, PostureRules: []string{"config/packs/painted-wolf/platform/host/posture-rules/build.yaml"}, ManifestRules: []string{"config/packs/painted-wolf/platform/host/posture-rules/spec.yaml"}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if !out.Allowed {
		t.Fatalf("build posture should not apply spec delegation rule, got %q", out.Code)
	}

	deny, err := engine.Evaluate(context.Background(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, ToolName: "delegate_dispatch"}, PostureRules: []string{"config/packs/painted-wolf/platform/host/posture-rules/spec.yaml"}, ManifestRules: []string{"config/packs/painted-wolf/platform/host/posture-rules/spec.yaml"}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if deny.Allowed {
		t.Fatal("expected spec delegation deny with deduped manifest rules")
	}
	if deny.Code != "SPEC_POSTURE_DELEGATION_FORBIDDEN" {
		t.Fatalf("code = %q", deny.Code)
	}
}

func TestPostureEngineEachRulesFileDenyRuleReachable(t *testing.T) {
	engine := newBundledPostureEngine(t)
	ctx := context.Background()
	cases := []struct {
		posture api.SessionPosture
		tool    string
		args    map[string]any
		allowed []string
		code    string
	}{
		{api.SessionPostureSpec, "delegate_dispatch", nil, nil, "SPEC_POSTURE_DELEGATION_FORBIDDEN"},
		{api.SessionPostureBuild, "task", map[string]any{"agent_type": "sentinel"}, []string{"implementer"}, "DISALLOWED_AGENT"},
		{api.SessionPostureOrchestrate, "task", map[string]any{"agent_type": "plan-writer"}, nil, "ORCHESTRATE_POSTURE_NO_PLAN_WRITER"},
		{api.SessionPostureVet, "delegate_dispatch", nil, nil, "VET_POSTURE_DELEGATION_FORBIDDEN"},
	}
	for _, tc := range cases {
		t.Run(string(tc.posture)+"/"+tc.tool, func(t *testing.T) {
			out, err := engine.Evaluate(ctx, rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: tc.posture, ToolName: tc.tool, ToolArgs: tc.args, AllowedAgents: tc.allowed}})
			testutil.FailErr(t, "engine.Evaluate failed", err)
			if out.Allowed {
				t.Fatal("expected deny")
			}
			if out.Code != tc.code {
				t.Fatalf("code = %q want %q", out.Code, tc.code)
			}
		})
	}
}

func newBundledPostureEngine(t *testing.T) *rules.PostureRuleEngine {
	t.Helper()
	postures, err := profiles.LoadPostureRegistry()
	testutil.FailErr(t, "profiles.LoadPostureRegistry failed", err)
	packs, err := rules.LoadBundledRules()
	testutil.FailErr(t, "rules.LoadBundledRules failed", err)
	if err := rules.ValidatePostureRules(postures, sessionposture.AllSessionPostures(), packs); err != nil {
		testutil.FailErr(t, "rules.ValidatePostureRules failed", err)
	}
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "register rule conditions", err)
	}
	engine, err := rules.NewPostureRuleEngine(postures, packs, reg)
	testutil.FailErr(t, "rules.NewPostureRuleEngine failed", err)
	return engine
}
