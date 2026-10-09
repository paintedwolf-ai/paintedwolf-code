package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowgates "github.com/lycaon/lycaon/internal/workflow/gates"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSyncHumanApprovalRejectsWhenNotReady(t *testing.T) {
	mgr, sessStore, _, projectDir := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := mgr.Starts.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0", Request: "test request",
	})
	testutil.FailErr(t, "StartHuman", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "completePlanResearchAtDepthNone", err)

	manifest, err := mgr.Resolver.ForRun(ctx, run)
	testutil.FailErr(t, "manifestForRun", err)
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars = runstate.SetHostVar(vars, "phase_skipped.review", true)
	vars, err = workflowphases.ApplyPhaseOnEnter(ctx, workflowphases.PhaseEnterRequest{
		Sessions: sessStore, SessionID: run.SessionID, Manifest: manifest, PhaseID: "approve",
		Vars: vars, BlueprintPath: run.BlueprintPath, Registry: mgr.Policy.Registry,
	})
	testutil.FailErr(t, "workflowphases.ApplyPhaseOnEnter approve", err)
	if conditions.DotPathTruthy(vars, "human_approval.ready") {
		t.Fatal("empty @plan must keep human_approval.ready=false")
	}
	run.CurrentPhase = "approve"
	testutil.FailErr(t, "CommitState", mgr.Store.State.CommitState(ctx, run, projectDir, vars))

	synced, err := mgr.Approvals.SyncHumanApproval(ctx, run.ID, projectDir)
	if !errors.Is(err, runstate.ErrHumanApprovalNotReady) {
		t.Fatalf("SyncHumanApproval err = %v want runstate.ErrHumanApprovalNotReady", err)
	}
	if synced == nil || synced.CurrentPhase != "approve" {
		t.Fatalf("run must stay on approve; got %+v", synced)
	}
	vars, err = mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars after reject", err)
	if workflowgates.SatisfiedInVars(vars, "human_approval") {
		t.Fatal("human_approval must not be satisfied when not ready")
	}
}

func TestPlanApprovalPreflightThenSyncAdvancesWhenReady(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	ctx := workflowCaller(t, mgr)
	run, err := mgr.Starts.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0", Request: "test request",
	})
	testutil.FailErr(t, "StartHuman", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "completePlanResearchAtDepthNone", err)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	run, err = advancePlanThroughExpand(ctx, mgr, run)
	testutil.FailErr(t, "advancePlanThroughExpand", err)
	run, err = completePlanReviewAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "completePlanReviewAtDepthNone", err)
	if run.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve", run.CurrentPhase)
	}

	testutil.FailErr(t, "ValidatePlanApprovalReady", mgr.Approvals.ValidatePlanApprovalReady(
		ctx, run.ProjectID, run.BlueprintPath, projectDir,
	))
	unchanged, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get after approval preflight", err)
	if unchanged.CurrentPhase != "approve" {
		t.Fatalf("preflight phase = %q want approve", unchanged.CurrentPhase)
	}
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars after preflight", err)
	if workflowgates.SatisfiedInVars(vars, "human_approval") {
		t.Fatal("approval preflight must not satisfy human_approval")
	}
	plan, err := blueprintMgr.Get(ctx, run.ProjectID, run.BlueprintPath)
	testutil.FailErr(t, "Get blueprint after preflight", err)
	if plan.Status != api.BlueprintStatusDraft {
		t.Fatalf("preflight blueprint status = %q want draft", plan.Status)
	}

	wakeCount := 0
	mgr.Approvals.OnHumanApprovalAdvanced = func(_ context.Context, advanced *api.WorkflowRun) {
		sessionID := advanced.SessionID
		if sessionID != run.SessionID {
			t.Fatalf("approval hook session=%q", sessionID)
		}
		wakeCount++
	}
	reviewed, err := blueprintMgr.Get(ctx, run.ProjectID, run.BlueprintPath)
	testutil.FailErr(t, "Get reviewed blueprint", err)
	synced, err := mgr.Approvals.ApprovePlan(ctx, run.ProjectID, run.BlueprintPath, projectDir, run.ID, unchanged.Revision, workflowdef.HashBlueprintContent(reviewed.Content))
	testutil.FailErr(t, "ApprovePlan", err)
	if synced.CurrentPhase == "approve" {
		t.Fatal("ready approve must auto-advance past approve")
	}
	if wakeCount != 1 {
		t.Fatalf("approval that synchronously starts a child must wake the active child; got %d", wakeCount)
	}
	_, err = mgr.Approvals.ApprovePlan(ctx, run.ProjectID, run.BlueprintPath, projectDir, run.ID, unchanged.Revision, workflowdef.HashBlueprintContent(reviewed.Content))
	testutil.FailErr(t, "repeat ApprovePlan", err)
	if wakeCount != 1 {
		t.Fatalf("repeat approval hook count = %d want 1", wakeCount)
	}
	approved, err := blueprintMgr.Get(ctx, run.ProjectID, run.BlueprintPath)
	testutil.FailErr(t, "Get approved blueprint", err)
	if approved.Status != api.BlueprintStatusApproved {
		t.Fatalf("approved blueprint status = %q", approved.Status)
	}
	changed := approved.Content + "\n"
	updated, err := blueprintMgr.Update(ctx, run.ProjectID, run.BlueprintPath, &changed, nil)
	testutil.FailErr(t, "edit approved blueprint", err)
	superseded, err := blueprintMgr.Get(ctx, run.ProjectID, updated.Path)
	testutil.FailErr(t, "Get edited blueprint", err)
	if superseded.Status != api.BlueprintStatusDraft {
		t.Fatalf("edited blueprint status = %q want draft", superseded.Status)
	}
}

func TestTrySatisfyHumanApprovalFromChatBailsWhenNotReady(t *testing.T) {
	mgr, sessStore, _, projectDir := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := mgr.Starts.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman options", err)
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	manifest, err := mgr.Resolver.ForRun(ctx, run)
	testutil.FailErr(t, "manifestForRun", err)
	vars, err = workflowphases.ApplyPhaseOnEnter(ctx, workflowphases.PhaseEnterRequest{
		Sessions: sessStore, SessionID: run.SessionID, Manifest: manifest, PhaseID: "select",
		Vars: vars, Registry: mgr.Policy.Registry,
	})
	testutil.FailErr(t, "workflowphases.ApplyPhaseOnEnter select", err)
	run.CurrentPhase = "select"
	testutil.FailErr(t, "CommitState", mgr.Store.State.CommitState(ctx, run, projectDir, vars))

	testutil.FailErr(t, "TryResolveUserFeedback", mgr.Feedback.TryResolveUserFeedback(ctx, run.SessionID, "", testutil.HostOwner().ID, "yes"))
	vars, err = mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars after chat", err)
	if workflowgates.SatisfiedInVars(vars, "human_approval") {
		t.Fatal("chat must not satisfy human_approval when not ready")
	}
}

func TestOptionsTerminalApprovalDoesNotWakeAmbientImplementation(t *testing.T) {
	mgr, sessStore, blueprintMgr, projectDir := testManagerWithRegistry(t)
	ctx := workflowCaller(t, mgr)
	run, err := mgr.Starts.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman options", err)
	content := "---\nstatus: draft\ncriterion: minimal surface\nwinner: package function\n---\nKeep the package API.\n"
	_, err = blueprintMgr.Update(ctx, run.ProjectID, run.BlueprintPath, &content, nil)
	testutil.FailErr(t, "write Options selection", err)

	manifest, err := mgr.Resolver.ForRun(ctx, run)
	testutil.FailErr(t, "manifestForRun", err)
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars, err = workflowphases.ApplyPhaseOnEnter(ctx, workflowphases.PhaseEnterRequest{
		Sessions: sessStore, SessionID: run.SessionID, Manifest: manifest, PhaseID: "select",
		Vars: vars, BlueprintPath: run.BlueprintPath, Registry: mgr.Policy.Registry,
	})
	testutil.FailErr(t, "workflowphases.ApplyPhaseOnEnter select", err)
	if !conditions.DotPathTruthy(vars, "human_approval.ready") {
		t.Fatal("materialized selection must be approval-ready")
	}
	run.CurrentPhase = "select"
	testutil.FailErr(t, "CommitState select", mgr.Store.State.CommitState(ctx, run, projectDir, vars))
	current, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get current run", err)
	reviewed, err := blueprintMgr.Get(ctx, run.ProjectID, run.BlueprintPath)
	testutil.FailErr(t, "Get reviewed blueprint", err)

	completedCount := 0
	mgr.Children.OnRunCompleted = func(_ context.Context, completed *api.WorkflowRun) {
		completedCount++
		if completed.ID != run.ID || completed.Status != api.WorkflowRunStatusComplete {
			t.Fatalf("unexpected completion: %+v", completed)
		}
	}
	humanWakeCount := 0
	phaseWakeCount := 0
	phaseEnterCount := 0
	mgr.Approvals.OnHumanApprovalAdvanced = func(context.Context, *api.WorkflowRun) { humanWakeCount++ }
	mgr.Publication.OnPhaseAutoAdvanced = func(context.Context, string, string, string, string) { phaseWakeCount++ }
	mgr.Phases.PhaseEnterHook = func(context.Context, *workflowphases.RunContext, workflowdef.PhaseDef) { phaseEnterCount++ }
	completed, err := mgr.Approvals.ApprovePlan(ctx, run.ProjectID, run.BlueprintPath, projectDir, run.ID, current.Revision, workflowdef.HashBlueprintContent(reviewed.Content))
	testutil.FailErr(t, "ApprovePlan", err)
	if completed.Status != api.WorkflowRunStatusComplete || completed.CurrentPhase != "done" {
		t.Fatalf("completed run = status %q phase %q", completed.Status, completed.CurrentPhase)
	}
	if completedCount != 1 {
		t.Fatalf("completion notifications=%d want 1", completedCount)
	}
	_, err = mgr.Approvals.ApprovePlan(ctx, run.ProjectID, run.BlueprintPath, projectDir, run.ID, current.Revision, workflowdef.HashBlueprintContent(reviewed.Content))
	testutil.FailErr(t, "replay approval", err)
	if completedCount != 1 {
		t.Fatalf("replayed approval repeated completion notification: %d", completedCount)
	}
	if humanWakeCount != 0 || phaseWakeCount != 0 || phaseEnterCount != 0 {
		t.Fatalf("terminal approval callbacks: human=%d phase=%d enter=%d", humanWakeCount, phaseWakeCount, phaseEnterCount)
	}
}
