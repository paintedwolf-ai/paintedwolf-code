package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPendingWorkflowStartFromScaffold(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	if err := mgr.NoteWorkflowStartProposal(ctx, sessionID, "plan", "1.0.0"); err != nil {
		testutil.FailErr(t, "NoteWorkflowStartProposal", err)
	}
	ui, err := mgr.ComputeSessionUI(ctx, sessionID)
	if err != nil {
		testutil.FailErr(t, "ComputeSessionUI", err)
	}
	if ui == nil || ui.PendingWorkflowStart == nil {
		t.Fatal("expected pending_workflow_start")
	}
	if ui.PendingWorkflowStart.WorkflowID != "plan" {
		t.Fatalf("workflow_id = %q", ui.PendingWorkflowStart.WorkflowID)
	}
}

func TestPendingWorkflowStartClearsWhenRunStarts(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	if err := mgr.NoteWorkflowStartProposal(ctx, sessionID, "plan", "1.0.0"); err != nil {
		testutil.FailErr(t, "NoteWorkflowStartProposal", err)
	}
	_, err := mgr.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0", Request: "test request",
	})
	testutil.FailErr(t, "StartHuman", err)
	ui, err := mgr.ComputeSessionUI(ctx, sessionID)
	if err != nil {
		testutil.FailErr(t, "ComputeSessionUI", err)
	}
	if ui != nil && ui.PendingWorkflowStart != nil {
		t.Fatal("expected no pending_workflow_start after run start")
	}
}

func TestComputeRunUIHumanApprovalNotAwaitingEmptyPlan(t *testing.T) {
	mgr, sessStore, _, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := mgr.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0", Request: "test request",
	})
	testutil.FailErr(t, "StartHuman", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "completePlanResearchAtDepthNone", err)
	// Enter approve with an empty plan.
	manifest, err := mgr.manifestForRun(ctx, run)
	testutil.FailErr(t, "manifestForRun", err)
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars = SetHostVar(vars, "phase_skipped.review", true)
	vars, err = ApplyPhaseOnEnter(ctx, PhaseEnterRequest{
		Sessions: sessStore, SessionID: run.SessionID, Manifest: manifest, PhaseID: "approve",
		Vars: vars, BlueprintPath: run.BlueprintPath, Registry: mgr.Registry,
	})
	testutil.FailErr(t, "ApplyPhaseOnEnter approve", err)
	run.CurrentPhase = "approve"
	if err := mgr.Store.CommitState(ctx, run, "", vars); err != nil {
		testutil.FailErr(t, "CommitState approve", err)
	}
	if conditions.DotPathTruthy(vars, "human_approval.ready") {
		t.Fatalf("empty @plan must keep human_approval.ready=false; vars=%v", vars)
	}
	ui, err := mgr.ComputeRunUI(ctx, run)
	testutil.FailErr(t, "ComputeRunUI", err)
	if ui != nil && ui.HumanApprovalAwaiting {
		t.Fatal("Den chrome must not await approval when host ready=false")
	}
}

func TestComputeRunUIHumanApprovalAwaiting(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := mgr.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0", Request: "test request",
	})
	if err != nil {
		testutil.FailErr(t, "StartHuman", err)
	}
	run = completePlanIntakeT(ctx, t, mgr, run)
	run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "completePlanResearchAtDepthNone", err)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	run, err = advancePlanThroughExpand(ctx, mgr, run)
	testutil.FailErr(t, "advancePlanThroughExpand", err)
	run, err = completePlanReviewAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "completePlanReviewAtDepthNone", err)
	ui, err := mgr.ComputeRunUI(ctx, run)
	if err != nil {
		testutil.FailErr(t, "ComputeRunUI", err)
	}
	if ui == nil || !ui.HumanApprovalAwaiting {
		t.Fatalf("expected human_approval_awaiting at approve; phase=%q ui=%+v", run.CurrentPhase, ui)
	}
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if !conditions.DotPathTruthy(vars, "human_approval.ready") {
		t.Fatalf("valid @plan + review_depth=none must set human_approval.ready; vars=%v", vars)
	}
}

func TestComputeRunUIHumanApprovalAwaitingWithoutTasksMarker(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := mgr.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0", Request: "test request",
	})
	if err != nil {
		testutil.FailErr(t, "StartHuman", err)
	}
	run = completePlanIntakeT(ctx, t, mgr, run)
	run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "completePlanResearchAtDepthNone", err)
	bp, err := blueprintMgr.Get(ctx, run.ProjectID, run.BlueprintPath)
	testutil.FailErr(t, "Get stub-only blueprint", err)
	if _, err := blueprintMgr.Store.UpdateContent(ctx, run.ProjectID, run.BlueprintPath, conditions.TestPlanContentStubOnly, blueprint.ContentDigest(bp.Content)); err != nil {
		testutil.FailErr(t, "plan update stub-only", err)
	}
	run, err = advancePlanThroughExpand(ctx, mgr, run)
	testutil.FailErr(t, "advancePlanThroughExpand", err)
	run, err = completePlanReviewAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "completePlanReviewAtDepthNone", err)
	ui, err := mgr.ComputeRunUI(ctx, run)
	if err != nil {
		testutil.FailErr(t, "ComputeRunUI", err)
	}
	if ui == nil || !ui.HumanApprovalAwaiting {
		t.Fatalf("expected human_approval_awaiting for stub-valid plan at approve; phase=%q", run.CurrentPhase)
	}
}

func TestComputeRunUIPlanRevisionAt(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	run, err := mgr.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0", Request: "test request",
	})
	if err != nil {
		testutil.FailErr(t, "StartHuman", err)
	}
	ui, err := mgr.ComputeRunUI(ctx, run)
	if err != nil {
		testutil.FailErr(t, "ComputeRunUI", err)
	}
	if ui == nil || ui.PlanRevisionAt == nil {
		t.Fatal("expected plan_revision_at on plan-family run with linked plan")
	}
}

func TestPlanParametersAndTrigger(t *testing.T) {
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	plan, err := reg.Get("plan", "1.0.0")
	testutil.FailErr(t, "Get plan", err)
	if _, ok := plan.Parameters["auto_approve"]; !ok {
		t.Fatal("plan must keep auto_approve parameter")
	}
	match, ok := reg.FindTriggerMatch("/plan")
	if !ok || match.Manifest.ID != "plan" {
		t.Fatalf("FindTriggerMatch /plan = %+v ok=%v", match, ok)
	}
}

func TestRegistryUsesSemanticVersionOrder(t *testing.T) {
	reg := workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		workflowdef.ManifestKey("demo", "2.0.0"):  {ID: "demo", Version: "2.0.0", Trigger: "/demo"},
		workflowdef.ManifestKey("demo", "10.0.0"): {ID: "demo", Version: "10.0.0", Trigger: "/demo"},
	})
	list := reg.List()
	if len(list) != 2 || list[0].Version != "10.0.0" {
		t.Fatalf("List versions = %+v, want 10.0.0 first", list)
	}
	match, ok := reg.FindTriggerMatch("/demo")
	if !ok || match.Manifest.Version != "10.0.0" {
		t.Fatalf("FindTriggerMatch = %+v ok=%v, want 10.0.0", match, ok)
	}
}

func TestApplyAutoApproveEffectsSkipsApproval(t *testing.T) {
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	plan, err := reg.Get("plan", "1.0.0")
	testutil.FailErr(t, "Get plan", err)
	vars := ApplyMergedParams(nil, map[string]string{
		"research_depth": "none",
		"auto_approve":   "true",
	})
	vars = ApplyAutoApproveEffects(vars, plan)
	if !conditions.DotPathTruthy(vars, "phase_skipped.approve") {
		t.Fatalf("auto_approve must skip approval; vars=%v", vars)
	}
}
