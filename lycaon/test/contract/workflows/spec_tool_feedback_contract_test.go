package contract

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolfeedback"
	"github.com/lycaon/lycaon/internal/toolrejection"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type specRejectScenario struct {
	name      string
	tool      string
	args      map[string]any
	plan      string
	vars      map[string]any
	phase     string
	wantCode  string
	wantPhase string
}

type specCompletionScenario struct {
	name     string
	input    guidance.EnrichInput
	wantCode string
}

func loadBundledHintConfig(t *testing.T) *guidance.HintConfig {
	t.Helper()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	return cfg
}

func TestSpecToolFeedbackRejectShape(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	engine := specPlanEngine(t)
	bp := phaseTestBlockPlane(t)
	formatter := guidance.NewToolRejectFormatter(guidance.NewFeedbackDeduper())

	stubInvalid := ""
	stubComplete := "---\ntitle: Ship it\nresearch_depth: light\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n\n## Plan implementation scope\n\n**Size:** s\n\n## Plan breaking changes\n\nnone\n\n## Approach\n\nship it\n"
	scopeBreaking := stubComplete
	expandPlan := scopeBreaking + "\n\n## Plan review depth\n\ndual\n\n<!-- lycaon:tasks\n[{\"id\":\"t1\",\"title\":\"Ship\"}]\n-->"
	stubMissingScope := "---\nresearch_depth: light\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n\n## Plan breaking changes\n\nnone\n"
	stubMissingBreaking := "---\nresearch_depth: light\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n\n## Plan implementation scope\n\n**Size:** s\n"
	allowed := []string{"plan-writer", "repo-researcher", "plan-reviewer", "implementer"}

	scenarios := []specRejectScenario{
		{
			name: "delegation forbidden smoke", tool: "plan_from_blueprint", plan: stubComplete,
			wantCode: "SPEC_POSTURE_DELEGATION_FORBIDDEN", wantPhase: "1",
		},
		{
			name: "state forbidden stub valid", tool: "state_create", plan: stubComplete,
			wantCode: "SPEC_POSTURE_STATE_FORBIDDEN", wantPhase: "2",
		},
		{
			name: "stub required plan writer", tool: "task", args: map[string]any{"agent_type": "plan-writer"},
			plan: stubInvalid, wantCode: "SPEC_POSTURE_STUB_REQUIRED", wantPhase: "1",
		},
		{
			name: "implementer forbidden", tool: "task", args: map[string]any{"agent_type": "implementer"},
			plan: expandPlan, wantCode: "SPEC_POSTURE_IMPLEMENT_FORBIDDEN", wantPhase: "6",
		},
		{
			name: "handoff not ready", tool: "handoff_implement", plan: scopeBreaking,
			wantCode: "SPEC_POSTURE_HANDOFF_FORBIDDEN", wantPhase: "6",
		},
		{
			name: "scope required", tool: "task", args: map[string]any{"agent_type": "repo-researcher"},
			plan: stubMissingScope, wantCode: "SPEC_POSTURE_STUB_REQUIRED", wantPhase: "1",
		},
		{
			name: "breaking required", tool: "task", args: map[string]any{"agent_type": "repo-researcher"},
			plan: stubMissingBreaking, wantCode: "SPEC_POSTURE_STUB_REQUIRED", wantPhase: "1",
		},
		{
			name: "stub required critic incomplete", tool: "task", args: map[string]any{"agent_type": "plan-reviewer"},
			plan: "## Goal\n\nx\n", vars: runstate.SetHostVar(nil, "phase_skipped.research", true),
			wantCode: "SPEC_POSTURE_STUB_REQUIRED", wantPhase: "1",
		},
		{
			name: "research required before critic", tool: "task", args: map[string]any{"agent_type": "plan-reviewer"},
			plan: expandPlan, phase: "expand", wantCode: "SPEC_POSTURE_RESEARCH_REQUIRED", wantPhase: "2",
		},
		{
			name: "not approved handoff", tool: "handoff_implement", plan: expandPlan, phase: "approve",
			wantCode: "SPEC_POSTURE_HANDOFF_FORBIDDEN", wantPhase: "6",
		},
		{
			name: "phase skipped research", tool: "task", args: map[string]any{"agent_type": "repo-researcher"},
			plan: scopeBreaking, vars: runstate.SetHostVar(nil, "phase_skipped.research", true),
			wantCode: "SPEC_POSTURE_PHASE_SKIPPED", wantPhase: "2",
		},
		{
			name: "disallowed agent", tool: "task", args: map[string]any{"agent_type": "outsider"},
			plan: stubComplete, wantCode: "DISALLOWED_AGENT", wantPhase: "",
		},
	}
	ctx := context.Background()
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			progress := guidance.ComputePlanProgress(sc.plan, guidance.PlanEvalFlagsFromVars(sc.vars), guidance.DispatchFromVars(sc.vars))
			out, err := engine.Evaluate(ctx, rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, Phase: sc.phase, ToolName: sc.tool, ToolArgs: sc.args, PlanContent: sc.plan, Vars: sc.vars, AllowedAgents: allowed}, PlanProgress: progress})
			contractcheck.FailErr(t, "engine.Evaluate failed", err)
			if out.Allowed {
				t.Fatal("expected deny")
			}
			code := out.RejectCode
			if code == "" {
				code = out.Code
			}
			if code != sc.wantCode {
				t.Fatalf("code = %q want %q", code, sc.wantCode)
			}
			if sc.wantPhase != "" && out.PhaseRequired != sc.wantPhase {
				t.Fatalf("phase = %q want %q", out.PhaseRequired, sc.wantPhase)
			}

			if sc.wantPhase == "" {
				return
			}
			rejected := bp.RejectObservation(ctx, sc.tool, "coordinator", sc.args, &toolrejection.ToolReject{Code: code})
			refusal, ok := guidance.RefusalFromError(rejected)
			if !ok || refusal.Copy == nil {
				t.Fatalf("host denial did not resolve through OAR: %v", rejected)
			}
			block, err := formatter.FormatBlock(ctx, "spec-session", sc.tool, specOutcomeView{out: *out}, progress, refusal.Copy)
			contractcheck.FailErr(t, "formatter.FormatBlock failed", err)
			if parsed := guidance.HostRejectCode(block); parsed != sc.wantCode {
				t.Fatalf("HostRejectCode = %q want %q", parsed, sc.wantCode)
			}
			if !strings.Contains(block, sc.tool) {
				t.Fatalf("block does not name the rejected tool %q:\n%s", sc.tool, block)
			}
		})
	}
}

func TestSpecToolFeedbackCompletionBannerShape(t *testing.T) {
	t.Parallel()
	hints := loadBundledHintConfig(t)
	enricher := guidance.NewToolOutputEnricher(hints, nil, nil)

	scenarios := []specCompletionScenario{
		{
			name: "find overflow narrow banner",
			input: guidance.EnrichInput{
				SessionID: "s1", Session: &api.Session{ID: "s1"},
				Tool:   "find",
				Output: `{"view":"digest","selected":0,"total":900,"truncated":true,"distribution":[{"kind":"ext","key":".go","count":500}]}`,
			},
			wantCode: "FIND_OVERFLOW_NARROW",
		},
		{
			name: "spec progress banner",
			input: guidance.EnrichInput{
				SessionID: "s1",
				Session:   &api.Session{ID: "s1", Posture: api.SessionPostureSpec},
				Tool:      "write", Output: "wrote plan section",
				PlanProgress: guidance.PlanProgress{
					PhaseInferred:     1,
					PhaseInferredName: "Research depth (stub plan file)",
					NextAction:        "write stub",
					ProgressChecklist: "[ ] Phase 1 — Research depth (stub plan file)\n",
					ChecklistHash:     "hash-progress-1",
				},
			},
			wantCode: "SPEC_POSTURE_PROGRESS",
		},
		{
			name: "board empty skip verify",
			input: guidance.EnrichInput{
				SessionID: "s1", Session: &api.Session{ID: "s1"},
				Tool: "pack_board", Output: `{"board_chars":0}`,
			},
			wantCode: "BOARD_EMPTY_SKIP_TO_VERIFY",
		},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			got := enricher.Enrich(t.Context(), sc.input)
			if !got.Facts.HasCode(sc.wantCode) {
				t.Fatalf("raised codes do not include %q: %+v", sc.wantCode, got.Facts)
			}
			if parsed := guidance.HostRejectCode(got.Output); parsed != sc.wantCode {
				t.Fatalf("emitted code = %q want %q:\n%s", parsed, sc.wantCode, got.Output)
			}
		})
	}
}

type specOutcomeView struct {
	out rules.RuleOutcome
}

func (v specOutcomeView) GetRejectCode() string    { return strings.TrimSpace(v.out.RejectCode) }
func (v specOutcomeView) GetPhaseRequired() string { return strings.TrimSpace(v.out.PhaseRequired) }
func (v specOutcomeView) GetPhaseRequiredName() string {
	return strings.TrimSpace(v.out.PhaseRequiredName)
}
func (v specOutcomeView) GetMinRequired() string { return strings.TrimSpace(v.out.MinRequired) }
func (v specOutcomeView) GetMaxPlaybook() string { return strings.TrimSpace(v.out.MaxPlaybook) }

func phaseTestBlockPlane(t *testing.T) *toolfeedback.BlockPlane {
	t.Helper()
	root := testutil.CheckoutRoot(t)
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallFile(filepath.Join(root, "lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml")))
	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	testutil.FailErr(t, "create loader", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "load policy", err)
	pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorToolRejected)
	return &toolfeedback.BlockPlane{Pipeline: pipeline, Renderer: oar.NewRenderer(nil, nil)}
}
