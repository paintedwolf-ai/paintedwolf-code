package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/workflow"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// specPlanGateCase mirrors spec-posture plan gate reject_codes shapes (code + phase + min_required).
type specPlanGateCase struct {
	name       string
	tool       string
	args       map[string]any
	plan       string
	vars       map[string]any
	phase      string
	wantCode   string
	wantPhase  string
	wantSubstr string
}

func specPlanEngine(t *testing.T) *rules.SimpleEngine {
	t.Helper()
	cfg, err := rules.LoadRulesConfig(config.PostureRulesDir.Join("spec.yaml"))
	contractcheck.FailErr(t, "load rules config YAML", err)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		contractcheck.FailErr(t, "register rule conditions", err)
	}
	return rules.NewSimpleEngine(cfg, reg)
}

func TestSpecPlanGateRejectCodesSubset(t *testing.T) {
	engine := specPlanEngine(t)
	stubInvalid := ""
	stubComplete := "---\ntitle: Ship it\nresearch_depth: light\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n\n## Plan implementation scope\n\n**Size:** s\n\n## Plan breaking changes\n\nnone\n\n## Approach\n\nship it\n"
	scopeBreaking := stubComplete
	expandPlan := scopeBreaking + "\n\n## Plan review depth\n\ndual\n\n<!-- lycaon:tasks\n[{\"id\":\"t1\",\"title\":\"Ship\"}]\n-->"
	expandApproved := expandPlan

	stubMissingScope := "---\nresearch_depth: light\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n\n## Plan breaking changes\n\nnone\n"
	stubMissingBreaking := "---\nresearch_depth: light\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n\n## Plan implementation scope\n\n**Size:** s\n"
	allowed := []string{"plan-writer", "repo-researcher", "plan-reviewer", "implementer"}

	cases := []specPlanGateCase{
		{"delegation forbidden", "delegate_dispatch", nil, stubComplete, nil, "", "SPEC_POSTURE_DELEGATION_FORBIDDEN", "1", "plan workflow"},
		{"state create stub invalid", "state_create", nil, stubInvalid, nil, "", "SPEC_POSTURE_STATE_FORBIDDEN", "1", "blueprints"},
		{"state create stub valid", "state_create", nil, stubComplete, nil, "", "SPEC_POSTURE_STATE_FORBIDDEN", "2", "repo-researcher"},
		{"plan writer stub invalid", "task", map[string]any{"agent_type": "plan-writer"}, stubInvalid, nil, "", "SPEC_POSTURE_STUB_REQUIRED", "1", "plan-writer"},
		{"task stub invalid", "task", map[string]any{"agent_type": "repo-researcher"}, stubInvalid, nil, "", "SPEC_POSTURE_STUB_REQUIRED", "1", "research Tasks"},
		{"implementer forbidden", "task", map[string]any{"agent_type": "implementer"}, expandPlan, nil, "", "SPEC_POSTURE_IMPLEMENT_FORBIDDEN", "6", "implementer"},
		{"handoff stub invalid", "handoff_implement", nil, stubInvalid, nil, "", "SPEC_POSTURE_HANDOFF_FORBIDDEN", "1", "stub"},
		{"handoff not ready", "handoff_implement", nil, scopeBreaking, nil, "", "SPEC_POSTURE_HANDOFF_FORBIDDEN", "6", "Phase 6"},
		{"scope required research", "task", map[string]any{"agent_type": "repo-researcher"}, stubMissingScope, nil, "", "SPEC_POSTURE_STUB_REQUIRED", "1", "research_depth"},
		{"breaking required research", "task", map[string]any{"agent_type": "repo-researcher"}, stubMissingBreaking, nil, "", "SPEC_POSTURE_STUB_REQUIRED", "1", "research_depth"},
		{"research phase skipped", "task", map[string]any{"agent_type": "repo-researcher"}, scopeBreaking, workflow.SetHostVar(nil, "phase_skipped.research", true), "", "SPEC_POSTURE_PHASE_SKIPPED", "2", "Tier"},
		{"stub required critic incomplete", "task", map[string]any{"agent_type": "plan-reviewer"}, "## Goal\n\nx\n", workflow.SetHostVar(nil, "phase_skipped.research", true), "", "SPEC_POSTURE_STUB_REQUIRED", "1", "critic"},
		{"review depth required", "task", map[string]any{"agent_type": "plan-reviewer"}, scopeBreaking, workflow.SetHostVar(nil, "phase_skipped.research", true), "", "SPEC_POSTURE_REVIEW_DEPTH_REQUIRED", "4", "Plan review depth"},
		{"research required before critic", "task", map[string]any{"agent_type": "plan-reviewer"}, expandPlan, nil, "expand", "SPEC_POSTURE_RESEARCH_REQUIRED", "2", "research"},
		{"critic phase skipped", "task", map[string]any{"agent_type": "plan-reviewer"}, expandPlan, workflow.SetHostVar(workflow.SetHostVar(nil, "phase_skipped.review", true), "research_satisfied", true), "", "SPEC_POSTURE_PHASE_SKIPPED", "5", "critics"},
		{"handoff not approved", "handoff_implement", nil, expandApproved, nil, "approve", "SPEC_POSTURE_HANDOFF_FORBIDDEN", "6", "approval"},
		{"research stub required", "task", map[string]any{"agent_type": "repo-researcher"}, stubInvalid, nil, "", "SPEC_POSTURE_STUB_REQUIRED", "1", "research"},
		{"critic stub required", "task", map[string]any{"agent_type": "plan-reviewer"}, stubInvalid, nil, "", "SPEC_POSTURE_STUB_REQUIRED", "1", "critic"},
		{"disallowed agent", "task", map[string]any{"agent_type": "outsider"}, stubComplete, nil, "", "DISALLOWED_AGENT", "", "allowlist"},
	}
	if len(cases) < 15 {
		t.Fatalf("scenario table too small: %d", len(cases))
	}

	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := engine.Evaluate(ctx, rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, Phase: tc.phase, ToolName: tc.tool, ToolArgs: tc.args, PlanContent: tc.plan, Vars: tc.vars, AllowedAgents: allowed}})
			contractcheck.FailErr(t, "engine.Evaluate failed", err)
			if out.Allowed {
				t.Fatal("expected deny")
			}
			if out.Code != tc.wantCode && out.RejectCode != tc.wantCode {
				t.Fatalf("code = %q reject = %q want %q", out.Code, out.RejectCode, tc.wantCode)
			}
			if tc.wantPhase != "" && out.PhaseRequired != tc.wantPhase {
				t.Fatalf("phase_required = %q want %q", out.PhaseRequired, tc.wantPhase)
			}
			if tc.wantSubstr != "" {
				msg := out.Message + " " + out.MinRequired
				if !strings.Contains(strings.ToLower(msg), strings.ToLower(tc.wantSubstr)) {
					t.Fatalf("message/min_required %q missing %q", msg, tc.wantSubstr)
				}
			}
		})
	}
}

func TestSpecDelegateDenyCarriesPlanProgress(t *testing.T) {
	engine := specPlanEngine(t)
	plan := "---\nresearch_depth: none\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n"
	progress := guidance.ComputePlanProgress(plan, guidance.PlanEvalFlags{}, guidance.DispatchSnapshot{})
	out, err := engine.Evaluate(context.Background(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, ToolName: "delegate_dispatch", PlanContent: plan}, PlanProgress: progress})
	contractcheck.FailErr(t, "engine.Evaluate failed", err)
	if out.Allowed || out.Code != "SPEC_POSTURE_DELEGATION_FORBIDDEN" {
		t.Fatalf("outcome = %+v", out)
	}
	if out.PhaseRequired == "" {
		t.Fatal("expected phase_required on deny outcome")
	}
}

func TestSpecPlanGateAllowsCriticOnReview(t *testing.T) {
	engine := specPlanEngine(t)
	expandPlan := "---\ntitle: Ship it\nresearch_depth: light\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n\n## Plan implementation scope\n\n**Size:** s\n\n## Plan breaking changes\n\nnone\n\n## Approach\n\nship it\n\n## Plan review depth\n\ndual\n\n<!-- lycaon:tasks\n[{\"id\":\"t1\",\"title\":\"Ship\"}]\n-->"
	allowed := []string{"plan-writer", "repo-researcher", "plan-reviewer", "implementer"}
	out, err := engine.Evaluate(context.Background(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, Phase: "review", ReviewLoopActive: true, ToolName: "task", ToolArgs: map[string]any{"agent_type": "plan-reviewer"}, PlanContent: expandPlan, AllowedAgents: allowed}})
	contractcheck.FailErr(t, "engine.Evaluate failed", err)
	if !out.Allowed {
		t.Fatalf("expected allow on review; code=%q message=%q", out.Code, out.Message)
	}
}
