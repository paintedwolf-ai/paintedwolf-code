package inject

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func renderTestActiveWorkflow(t *testing.T, runCtx api.CoordinatorRunContext, snap WorkflowRuntimeSnapshot, hints *guidance.HintConfig, codes []string) string {
	t.Helper()
	renderer := testInjectRenderer(t)
	block, err := RenderActiveWorkflowInject(context.Background(), renderer, "sess-inject-test", CoordinatorTurnFrame{RunContext: runCtx, Runtime: snap}, hints, codes, nil)
	testutil.FailErr(t, "RenderActiveWorkflowInject failed", err)
	return block
}

func TestActiveWorkflowInjectIncludesWorkflowID(t *testing.T) {
	ctx := api.CoordinatorRunContext{WorkflowID: "wf-1", RunID: "run-1", CoordinatorBrief: "brief"}
	block := renderTestActiveWorkflow(t, ctx, WorkflowRuntimeSnapshot{}, nil, nil)
	if !strings.Contains(block, "wf-1") {
		t.Fatalf("block = %q", block)
	}
}

func TestActiveWorkflowInjectMakesInheritedApprovalAuthoritative(t *testing.T) {
	block := renderTestActiveWorkflow(t, api.CoordinatorRunContext{WorkflowID: "implement"}, WorkflowRuntimeSnapshot{
		BlueprintApproval: &BlueprintApprovalView{
			Status: "approved", Origin: "inherited", ParentRunID: "parent-1",
		},
	}, nil, nil)
	for _, want := range []string{
		"blueprint_approval", "`approved`", "inherited from parent run `parent-1`",
		"Execute the approved Blueprint now", "Do not ask the user to approve it again",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("missing %q in:\n%s", want, block)
		}
	}
}

func TestActiveWorkflowInjectIncludesComposeBrief(t *testing.T) {
	ctx := api.CoordinatorRunContext{WorkflowID: "wf-1", CoordinatorBrief: "Use pack topology."}
	block := renderTestActiveWorkflow(t, ctx, WorkflowRuntimeSnapshot{}, nil, nil)
	if !strings.Contains(block, "Use pack topology.") {
		t.Fatalf("block = %q", block)
	}
}

func TestActiveWorkflowInjectFreeChatOmitsWorkflow(t *testing.T) {
	block := renderTestActiveWorkflow(t, api.CoordinatorRunContext{}, WorkflowRuntimeSnapshot{}, nil, nil)
	if strings.Contains(block, "**workflow**") {
		t.Fatalf("unexpected workflow line in %q", block)
	}
}

func TestActiveWorkflowInjectIncludesGateHint(t *testing.T) {
	ctx := api.CoordinatorRunContext{WorkflowID: "wf-1", FailedLeaves: []string{"human_approval"}}
	cfg := &guidance.HintConfig{HintCodes: map[string]guidance.HintEntry{
		"WORKFLOW_GATE_UNMET": {Message: "gate blocked", Fix: "fix gates"},
	}}
	block := renderTestActiveWorkflow(t, ctx, WorkflowRuntimeSnapshot{}, cfg, []string{"WORKFLOW_GATE_UNMET"})
	if !strings.Contains(block, "WORKFLOW_GATE_UNMET") || !strings.Contains(block, "failed_leaves") {
		t.Fatalf("block = %q", block)
	}
}

func TestActiveWorkflowInjectIncludesRuntimeSections(t *testing.T) {
	runCtx := api.CoordinatorRunContext{
		WorkflowID: "default-pipeline", CurrentPhase: "research", RunStatus: "running",
	}
	snap := WorkflowRuntimeSnapshot{
		Topology: "default-pipeline",
		Phases:   []WorkflowPhaseRow{{ID: "research", CompleteWhen: "topology_stage_complete", BindTopologyStage: "research"}},
	}
	block := renderTestActiveWorkflow(t, runCtx, snap, nil, nil)
	for _, want := range []string{"completes on", "topology_phase_id", "Workflow phases", "(current)"} {
		if !strings.Contains(block, want) {
			t.Fatalf("missing %q in:\n%s", want, block)
		}
	}
}

func TestActiveWorkflowInjectRendersFlowBrief(t *testing.T) {
	runCtx := api.CoordinatorRunContext{WorkflowID: "plan", CurrentPhase: "review", RunStatus: "running"}
	snap := WorkflowRuntimeSnapshot{
		Phases: []WorkflowPhaseRow{
			{ID: "research", Next: "review"},
			{ID: "review", CompleteWhen: "gates_satisfied", Next: "approve",
				Gates: []WorkflowGateState{{ID: "evidence_passed:plan_review", Satisfied: false}}},
			{ID: "approve", Terminal: true},
		},
		PhaseExit: &PhaseExitView{
			Kind:                "proof",
			CoordinatorAdvances: true,
			OpenGates:           []string{"evidence_passed:plan_review"},
		},
	}
	block := renderTestActiveWorkflow(t, runCtx, snap, nil, nil)
	for _, want := range []string{
		"phase 2/3 — `review`",
		"· terminal",
		"Gates:",
		"[ ] `evidence_passed:plan_review`",
		"### Phase exit",
		"call `workflow_advance`",
		"Next: `approve`",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("missing %q in:\n%s", want, block)
		}
	}
}

func TestActiveWorkflowInjectAutoAdvancePhaseOmitsAdvanceCall(t *testing.T) {
	runCtx := api.CoordinatorRunContext{WorkflowID: "wf", CurrentPhase: "build"}
	snap := WorkflowRuntimeSnapshot{
		Phases: []WorkflowPhaseRow{
			{ID: "build", Gates: []WorkflowGateState{{ID: "child_run_complete", Satisfied: false}}},
		},
		PhaseExit: &PhaseExitView{
			Kind: "invoke",
		},
	}
	block := renderTestActiveWorkflow(t, runCtx, snap, nil, nil)
	if !strings.Contains(block, "### Phase exit") {
		t.Fatalf("missing Phase exit in:\n%s", block)
	}
	if !strings.Contains(block, "host advances on its own") {
		t.Fatalf("missing auto-advance copy in:\n%s", block)
	}
	if strings.Contains(block, "Advance: call") {
		t.Fatalf("must not use standalone Advance one-liner:\n%s", block)
	}
}

func TestActiveWorkflowInjectMakesStampedFanoutActionable(t *testing.T) {
	runCtx := api.CoordinatorRunContext{WorkflowID: "recon-pack", CurrentPhase: "execute"}
	snap := WorkflowRuntimeSnapshot{
		FanoutPlan: "1. **scout** — Inspect the workflow runtime",
		Phases: []WorkflowPhaseRow{{
			ID:    "execute",
			Gates: []WorkflowGateState{{ID: "worker_cycle_ready", Dormant: true}},
		}},
		PhaseExit: &PhaseExitView{
			Kind:         "proof",
			DormantGates: []string{"worker_cycle_ready"},
		},
	}
	block := renderTestActiveWorkflow(t, runCtx, snap, nil, nil)
	for _, want := range []string{
		"### Stamped fan-out",
		"stamped, not automatically dispatched",
		"call `task`",
		"as `agent_type`",
		"all_workers_idle",
		"do not duplicate them",
		"Inspect the workflow runtime",
		"dormant", "not failing",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("missing %q in:\n%s", want, block)
		}
	}
}

func TestActiveWorkflowInjectIncludesFeedbackPhases(t *testing.T) {
	ctx := api.CoordinatorRunContext{
		FeedbackPhases: []api.ComposeFeedbackPhase{{ID: "clarify", Prompt: "pick db"}},
	}
	block := renderTestActiveWorkflow(t, ctx, WorkflowRuntimeSnapshot{}, nil, nil)
	if !strings.Contains(block, "feedback_phases") || !strings.Contains(block, "clarify") {
		t.Fatalf("block = %q", block)
	}
}

func TestActiveWorkflowInjectListsChoiceTransitions(t *testing.T) {
	runCtx := api.CoordinatorRunContext{WorkflowID: "choice", CurrentPhase: "decide", RunStatus: "running"}
	snap := WorkflowRuntimeSnapshot{
		Phases: []WorkflowPhaseRow{{ID: "decide"}},
		PhaseExit: &PhaseExitView{
			Kind:          "human_approval",
			HumanApproval: true,
			ChoiceTransitions: []PhaseExitChoiceArm{
				{ID: "deepen", Label: "Deepen research", Actors: []string{"human", "coordinator"}, CoordinatorMayFire: true},
				{ID: "critique", Label: "Run critique", Actors: []string{"human", "coordinator"}, CoordinatorMayFire: true},
			},
		},
	}
	block := renderTestActiveWorkflow(t, runCtx, snap, nil, nil)
	for _, want := range []string{
		"Choice transitions out of this phase",
		"`deepen`",
		"`critique`",
		"workflow_transition",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("missing %q in:\n%s", want, block)
		}
	}
}

func TestRenderActiveWorkflowInject_NilRenderer(t *testing.T) {
	_, err := RenderActiveWorkflowInject(context.Background(), nil, "sess-inject-test", CoordinatorTurnFrame{}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("err = %v", err)
	}
}

func TestRenderActiveWorkflowInject_MissingSentinel(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	if err := engine.Register("inject/active-workflow.md", "## broken"); err != nil {
		testutil.FailErr(t, "engine.Register failed", err)
	}
	renderer := prompts.NewInjectRenderer(engine)
	_, err := RenderActiveWorkflowInject(context.Background(), renderer, "sess-inject-test", CoordinatorTurnFrame{RunContext: api.CoordinatorRunContext{WorkflowID: "wf"}}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), ActiveWorkflowInjectSentinel) {
		t.Fatalf("err = %v", err)
	}
}

func TestWorkflowInjectNeverAdvertisesExcludedAgents(t *testing.T) {
	declared := spawn.AmbientAllowedAgents()
	roster := ResolveAgentRoster("implement_investigate", declared, 1, true, true)
	if len(roster.Excluded) == 0 {
		t.Fatal("fixture must exclude at least one agent on an empty repo")
	}
	frame := CoordinatorTurnFrame{Roster: &roster}
	frame.RunContext.WorkflowID = "wf-1"
	frame.RunContext.RunID = "run-1"
	frame.RunContext.AllowedAgents = declared
	renderer := testInjectRenderer(t)
	block, err := RenderActiveWorkflowInject(context.Background(), renderer, "sess-inject-test", frame, nil, nil, nil)
	testutil.FailErr(t, "RenderActiveWorkflowInject failed", err)

	var allowedLine string
	for _, line := range strings.Split(block, "\n") {
		if strings.Contains(line, "**allowed_agents**") {
			allowedLine = line
		}
	}
	if allowedLine == "" {
		t.Fatalf("allowed_agents line missing in:\n%s", block)
	}
	for _, ex := range roster.Excluded {
		if strings.Contains(allowedLine, "`"+ex.Name+"`") {
			t.Fatalf("excluded agent %q advertised as allowed: %s", ex.Name, allowedLine)
		}
		if !strings.Contains(block, "`"+ex.Name+"` ("+ex.Code+")") {
			t.Fatalf("excluded agent %q not disclosed with code %q in:\n%s", ex.Name, ex.Code, block)
		}
	}
	if !strings.Contains(block, "unavailable this turn") {
		t.Fatalf("missing unavailable disclosure in:\n%s", block)
	}
}

func renderTestImplementSpawn(t *testing.T, allowedAgents []string) string {
	t.Helper()
	renderer := testInjectRenderer(t)
	block, err := RenderImplementSpawnInject(context.Background(), renderer, "sess-inject-test", allowedAgents, spawn.MaxInFlightTaskWorkers, spawn.SurfaceImplementSynthesis, 1, false, true,
		nil, nil)
	testutil.FailErr(t, "RenderImplementSpawnInject failed", err)
	return block
}

func TestImplementSpawnInjectIncludesSpawnMatrix(t *testing.T) {
	block := renderTestImplementSpawn(t, spawn.AmbientAllowedAgents())
	for _, want := range []string{
		ImplementSpawnInjectSentinel,
		"Spawn roster",
		"`implementer`",
		"`repo-researcher`",
		"`path-explorer`",
		"`code-reviewer`",
		"DISALLOWED_AGENT",
		"plan-writer",
		"Product edits only — dispatch with scope.mode write + paths; host rejects read/default. Read-only survey → repo-researcher",
		"Bounded path, find, and grep code survey",
		"(no command)",
		fmt.Sprintf("**%d** in-flight", spawn.MaxInFlightTaskWorkers),
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("missing %q in:\n%s", want, block)
		}
	}
}

func TestRenderImplementSpawnInject_NilRenderer(t *testing.T) {
	_, err := RenderImplementSpawnInject(context.Background(), nil, "sess-inject-test", nil, 0, "", 0, false, true,
		nil, nil)
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("err = %v", err)
	}
}

func TestRenderImplementSpawnInject_MissingSentinel(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	if err := engine.Register("inject/implement-spawn.md", "## broken"); err != nil {
		testutil.FailErr(t, "engine.Register failed", err)
	}
	renderer := prompts.NewInjectRenderer(engine)
	_, err := RenderImplementSpawnInject(context.Background(), renderer, "sess-inject-test", spawn.AmbientAllowedAgents(), spawn.MaxInFlightTaskWorkers, spawn.SurfaceImplementSynthesis, 1, false, true,
		nil, nil)
	if err == nil || !strings.Contains(err.Error(), ImplementSpawnInjectSentinel) {
		t.Fatalf("err = %v", err)
	}
}

func TestReportDocumentInstructionsOnlyInEnabledPhase(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		block := renderTestActiveWorkflow(t, api.CoordinatorRunContext{WorkflowID: "fixture", RunID: "run-1"}, WorkflowRuntimeSnapshot{ReportDocumentEnabled: enabled}, nil, nil)
		for _, field := range []string{`"headline"`, `"findings"`, `"limits"`} {
			if strings.Contains(block, field) != enabled {
				t.Fatalf("document field %s shown=%v, want shown only in an enabled report phase (enabled=%v)", field, !enabled, enabled)
			}
		}
	}
}
