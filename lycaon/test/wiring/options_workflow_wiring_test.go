package wiring

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"os"
	"path/filepath"
	"testing"
)

func TestOptionsSlashRequestStartsFanOut(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := context.Background()
	dir := h.ProjectDir(t, "options-slash")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureOrchestrate}, dir)
	testutil.FailErr(t, "create session", err)
	_, handled, err := h.WorkflowMgr.Slash.TrySlashPrompt(ctx, sess.ID, "/options pick postgres over sqlite", "")
	testutil.FailErr(t, "TrySlashPrompt /options", err)
	if !handled {
		t.Fatal("expected /options slash handled")
	}
	run, err := h.WorkflowMgr.Store.Runs.ActiveBySession(ctx, sess.ID)
	testutil.FailErr(t, "GetActive", err)
	vars, err := h.WorkflowMgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if got, _ := runstate.DotPathString(vars, "workflow_request.text"); got != "pick postgres over sqlite" {
		t.Fatalf("request = %q", got)
	}
	if got, _ := runstate.DotPathString(vars, "workflow_request.status"); got != "resolved" {
		t.Fatalf("request status = %q want resolved", got)
	}
	waitTopologyStageComplete(t, h.WorkflowMgr, run.ID, orchestration.TopologyBindStageFanOut)
}

func TestOptionsWorkflowEndToEnd(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := h.OwnerCtx(t, context.Background())
	dir := h.ProjectDir(t, "options")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureOrchestrate}, dir)
	testutil.FailErr(t, "create session", err)

	run, err := h.WorkflowMgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman options", err)
	if run.CurrentPhase != "fan_out" {
		t.Fatalf("phase = %q want fan_out", run.CurrentPhase)
	}

	// Pending request input holds the initial topology phase.
	h.Server.Admin.Workflow.Topology.StartOrchestratedTopologyForRun(ctx, sess.ID, run)
	if _, ok := h.DelegationStore.DelegationBySessionID(sess.ID); ok {
		t.Fatal("research fan-out started before the request was answered")
	}

	run, err = h.WorkflowMgr.Feedback.ResolveUserFeedback(ctx, sess.ID, run.ID, "workflow_request", "Pick the persistence layer; must stay embeddable and avoid new deps.")
	testutil.FailErr(t, "ResolveUserFeedback request", err)
	if run.CurrentPhase != "fan_out" && run.CurrentPhase != "judge" {
		t.Fatalf("phase = %q want fan_out or completed fan-out after request", run.CurrentPhase)
	}

	// Resolution launches the bound topology.
	waitTopologyStageComplete(t, h.WorkflowMgr, run.ID, orchestration.TopologyBindStageFanOut)
	waitWorkflowPhase(t, ctx, h.WorkflowMgr, run.ID, "judge")
	settleCtx := testutil.BoundedContext(t, topologyWaitBudget)
	h.Server.WaitForBackground(settleCtx)
	if err := settleCtx.Err(); err != nil {
		t.Fatalf("topology background work did not settle: %v", err)
	}

	assertManifestBoundSurface(t, h, ctx, sess, surface.HostLoopWakeSentinel, "decision_adjudicate")

	legs := fanOutLegsForSession(t, h, sess.ID)
	assertFanOutWorkersMatchTopology(t, h, run.ID, legs)
	scaffoldTopologyOutput(t, h.WorkflowMgr, run.ID, orchestration.TopologyBindStageFanOut)

	vars, err := h.WorkflowMgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars after fan_out", err)
	if got, _ := runstate.DotPathString(vars, "options.criterion"); got == "" {
		t.Fatal("options.criterion missing after fan_out")
	}

	installOptionsSelectionReport(t, dir, run.BlueprintPath, vars)

	// A non-terminal verdict keeps the judge phase open.
	needsRevision := map[string]string{"verdict": "NEEDS_REVISION", "winner": "undecided", "rationale": "the skeptic surfaced an unaddressed failure mode; re-research."}
	_, err = h.WorkflowMgr.Verdicts.RecordReviewLoopVerdict(ctx, sess.ID, needsRevision, nil, nil)
	testutil.FailErr(t, "RecordReviewLoopVerdict NEEDS_REVISION", err)
	run, err = h.WorkflowMgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get run after NEEDS_REVISION", err)
	if run.CurrentPhase != "judge" {
		t.Fatalf("phase = %q want judge (non-terminal verdict must hold)", run.CurrentPhase)
	}

	skepticChild := appendSucceededReviewAgent(t, h, ctx, sess, "skeptic", "")

	// A grounded terminal verdict advances the judge phase.
	verdict := map[string]string{"verdict": "SELECTED", "winner": "Approach B", "rationale": "B survives the skeptic on integration boundaries."}
	selected, err := h.WorkflowMgr.Verdicts.RecordReviewLoopVerdict(ctx, sess.ID, verdict,
		[]api.CitationGroundingCitedEvidence{reviewerCitation(skepticChild, "skeptic")}, nil)
	testutil.FailErr(t, "RecordReviewLoopVerdict SELECTED", err)
	run, err = h.WorkflowMgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get run after judge verdict", err)
	if run.CurrentPhase != "select" {
		t.Fatalf("phase = %q want select (terminal verdict should advance); verdict outcome = %+v", run.CurrentPhase, selected)
	}

	run, err = h.WorkflowMgr.Approvals.SyncHumanApproval(ctx, run.ID, dir)
	testutil.FailErr(t, "SyncHumanApproval", err)
	if run.CurrentPhase != "done" {
		t.Fatalf("phase = %q want done", run.CurrentPhase)
	}

	run, err = h.WorkflowMgr.Phases.TryAutoAdvance(ctx, run.ID)
	testutil.FailErr(t, "TryAutoAdvance done", err)
	run, err = h.WorkflowMgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get run after select", err)
	if run.Status != api.WorkflowRunStatusComplete {
		t.Fatalf("status = %q want complete (phase=%q)", run.Status, run.CurrentPhase)
	}
}

func installOptionsSelectionReport(t *testing.T, projectDir, blueprintPath string, vars map[string]any) {
	t.Helper()
	criterion, _ := runstate.DotPathString(vars, "options.criterion")
	content := "---\ncriterion: " + criterion + "\nwinner: Approach B\n---\n\n# Selection\n\nApproach B wins on integration boundaries.\n"
	path := filepath.Join(projectDir, filepath.FromSlash(blueprintPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		testutil.FailErr(t, "mkdir overlay", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		testutil.FailErr(t, "write selection report", err)
	}
}
