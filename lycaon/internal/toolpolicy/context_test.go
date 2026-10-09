package toolpolicy

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordingWorkflowView struct {
	calls     []string
	activeRun *api.WorkflowRun
}

func (r *recordingWorkflowView) snapshot(context.Context, string) (WorkflowSnapshot, error) {
	r.calls = append(r.calls, "snapshot")
	state := WorkflowSnapshot{Phase: "phase-a", AllowedAgents: []string{"coordinator"},
		ManifestRules: []string{"manifest-rules.yaml"}, BlueprintPath: "plan-1", PlanContent: "plan body", Vars: map[string]any{"k": "v"}}
	if r.activeRun != nil {
		state.RunID, state.WorkflowID, state.RunStatus = r.activeRun.ID, r.activeRun.WorkflowID, r.activeRun.Status
	}
	return state, nil
}

func TestBuildEvalContextUsesWorkflowView(t *testing.T) {
	view := &recordingWorkflowView{}
	deps := EngineDeps{Workflows: view.snapshot}
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureSpec}
	eval, err := BuildEvalContext(context.Background(), deps, sess, "read_file", map[string]any{"path": "x"})
	if err != nil {
		t.Fatalf("build evaluation context: %v", err)
	}
	if eval.Phase != "phase-a" {
		t.Fatalf("phase = %q want phase-a", eval.Phase)
	}
	if len(eval.AllowedAgents) != 1 || eval.AllowedAgents[0] != "coordinator" {
		t.Fatalf("allowed agents = %v", eval.AllowedAgents)
	}
	if len(eval.ManifestRules) != 1 || eval.ManifestRules[0] != "manifest-rules.yaml" {
		t.Fatalf("manifest rules = %v", eval.ManifestRules)
	}
	if eval.BlueprintPath != "plan-1" || eval.PlanContent != "plan body" {
		t.Fatalf("plan = %q %q", eval.BlueprintPath, eval.PlanContent)
	}
	if eval.Vars["k"] != "v" {
		t.Fatalf("vars = %v", eval.Vars)
	}
	if len(view.calls) != 1 {
		t.Fatalf("workflow view calls = %v", view.calls)
	}

}

func TestBuildEvalContextWiresActiveRunStatus(t *testing.T) {
	run := &api.WorkflowRun{
		ID: "run-1", WorkflowID: "plan", Status: api.WorkflowRunStatusRunning, CurrentPhase: "stub",
	}
	view := &recordingWorkflowView{activeRun: run}
	deps := EngineDeps{Workflows: view.snapshot}
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureSpec}
	eval, err := BuildEvalContext(context.Background(), deps, sess, "workflow_advance", nil)
	if err != nil {
		t.Fatalf("build evaluation context: %v", err)
	}
	if eval.WorkflowID != "plan" || eval.RunStatus != api.WorkflowRunStatusRunning {
		t.Fatalf("workflow = %q status = %q", eval.WorkflowID, eval.RunStatus)
	}
}

func TestBuildEvalContextUsesBoundCoordinatorTurnFrame(t *testing.T) {
	view := &recordingWorkflowView{}
	deps := EngineDeps{Workflows: view.snapshot}
	sess := &api.Session{ID: "s1", WorkspacePath: "/project", Posture: api.SessionPostureSpec}
	frame := &inject.CoordinatorTurnFrame{
		WorkflowRevision: 41,
		RunContext: api.CoordinatorRunContext{
			WorkflowID: "plan", RunID: "run-1", RunStatus: string(api.WorkflowRunStatusRunning),
			CurrentPhase: "verify", AllowedAgents: []string{"implementer"},
		},
		ManifestRules:    []string{"workflow-rules.yaml"},
		ScaffoldVars:     map[string]any{"dispatch": "sealed"},
		PostureRules:     []string{"postures/spec.yaml"},
		ProjectRootCount: 2,
		OverlayRootPaths: []string{"/project/.paintedwolf"},
		Runtime: inject.WorkflowRuntimeSnapshot{
			Blueprint:     &inject.BlueprintView{Path: "plan.md"},
			BlueprintBody: "# plan",
			PhaseExit:     &inject.PhaseExitView{ReviewLoopKey: "review"},
		},
	}

	eval, err := BuildEvalContext(WithCoordinatorTurnFrame(context.Background(), frame), deps, sess, "task", nil)
	if err != nil {
		t.Fatalf("build evaluation context: %v", err)
	}
	if len(view.calls) != 0 {
		t.Fatalf("frame-backed evaluation reloaded workflow state: %v", view.calls)
	}
	if eval.Phase != "verify" || eval.WorkflowRunID != "run-1" || eval.RunStatus != api.WorkflowRunStatusRunning {
		t.Fatalf("workflow identity = phase %q run %q status %q", eval.Phase, eval.WorkflowRunID, eval.RunStatus)
	}
	if len(eval.AllowedAgents) != 1 || eval.AllowedAgents[0] != "implementer" {
		t.Fatalf("allowed agents = %v", eval.AllowedAgents)
	}
	if len(eval.ManifestRules) != 1 || eval.ManifestRules[0] != "workflow-rules.yaml" {
		t.Fatalf("manifest rules = %v", eval.ManifestRules)
	}
	if len(eval.PostureRules) != 1 || eval.PostureRules[0] != "postures/spec.yaml" {
		t.Fatalf("posture rules = %v", eval.PostureRules)
	}
	if !eval.ReviewLoopActive || eval.BlueprintPath != "plan.md" || eval.PlanContent != "# plan" {
		t.Fatalf("frame policy state = %+v", eval)
	}
	if eval.Vars["dispatch"] != "sealed" {
		t.Fatalf("vars = %v", eval.Vars)
	}
	if eval.ProjectRootCount != 2 || len(eval.OverlayRootPaths) != 1 || eval.OverlayRootPaths[0] != "/project/.paintedwolf" {
		t.Fatalf("root policy state = count %d paths %v", eval.ProjectRootCount, eval.OverlayRootPaths)
	}
}

// TestBuildEvalContextEmptyAttachedRosterIsNotDeclaredFallback: an attached-but-empty
// turn roster means "no agents dispatchable" and must never fall back to the broader
// declared workflow allowlist (the repoKnownEmpty fail-open).
func TestBuildEvalContextEmptyAttachedRosterIsNotDeclaredFallback(t *testing.T) {
	view := &recordingWorkflowView{}
	deps := EngineDeps{Workflows: view.snapshot}
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureBuild}

	ctx := WithTaskSpawnAllowlist(context.Background(), []string{})
	eval, err := BuildEvalContext(ctx, deps, sess, "task", map[string]any{"agent_type": "implementer"})
	if err != nil {
		t.Fatalf("build evaluation context: %v", err)
	}
	if eval.AllowedAgents == nil {
		t.Fatal("attached empty roster must stay non-nil (no agents dispatchable)")
	}
	if len(eval.AllowedAgents) != 0 {
		t.Fatalf("allowed agents = %v, want empty", eval.AllowedAgents)
	}
}

// TestTaskSpawnAllowlistContextEmptyAttaches pins the nil-vs-empty contract of the ctx
// carrier itself.
func TestTaskSpawnAllowlistContextEmptyAttaches(t *testing.T) {
	if _, ok := TaskSpawnAllowlistFromContext(context.Background()); ok {
		t.Fatal("no attach must read as absent")
	}
	ctx := WithTaskSpawnAllowlist(context.Background(), nil)
	agents, ok := TaskSpawnAllowlistFromContext(ctx)
	if !ok {
		t.Fatal("an attached roster must read as present even when empty")
	}
	if agents == nil || len(agents) != 0 {
		t.Fatalf("agents = %#v, want empty non-nil", agents)
	}
}
