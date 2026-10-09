//go:build integration

package workflow

import (
	"context"
	"errors"
	workflowinputs "github.com/lycaon/lycaon/internal/workflow/inputs"
	"testing"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPlanImplementPhaseInvokesImplementChild(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "invoke.db")

	sessStore := store.NewSQL(sqlDB)
	dir := t.TempDir()
	blueprintStore := blueprint.NewFileStoreForTest(dir)
	blueprintMgr := blueprint.NewManager(blueprintStore)
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	wfStore := testRunStore(t, sqlDB)
	mgr := NewManager(wfStore, sessStore, reg, nil)
	WireBlueprintDepsForTest(mgr, dir)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDepsWithEvidence())

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create session", err)

	parent, err := mgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{WorkflowID: "plan", WorkflowVersion: "1.0.0", Request: "test request"})
	testutil.FailErr(t, "Start plan", err)
	parent = completePlanIntakeT(ctx, t, mgr, parent)
	seedValidPlanContent(t, blueprintMgr, parent.BlueprintPath)
	parent, err = advancePlanToApprovePhase(ctx, mgr, parent)
	testutil.FailErr(t, "advancePlanToApprovePhase", err)
	parent, err = mgr.Approvals.SyncHumanApproval(workflowCaller(t, mgr), parent.ID, dir)
	testutil.FailErr(t, "SyncHumanApproval", err)

	parentRow, err := wfStore.Runs.Get(ctx, parent.ID)
	testutil.FailErr(t, "Get parent", err)
	if parentRow.Status != api.WorkflowRunStatusPausedOnChild {
		t.Fatalf("parent status = %q want paused_on_child", parentRow.Status)
	}
	if parentRow.CurrentPhase != "execute" {
		t.Fatalf("parent phase = %q want execute", parentRow.CurrentPhase)
	}

	active, err := wfStore.Runs.ActiveBySession(ctx, sess.ID)
	testutil.FailErr(t, "ActiveBySession", err)
	if active == nil {
		t.Fatal("expected active child run")
	}
	if active.ParentRunID == nil || *active.ParentRunID != parent.ID {
		t.Fatalf("active run parent = %v want %q", active.ParentRunID, parent.ID)
	}
	if active.WorkflowID != "implement" {
		t.Fatalf("active workflow_id = %q want implement", active.WorkflowID)
	}
	if active.BlueprintPath != parent.BlueprintPath {
		t.Fatalf("child BlueprintPath = %q want inherited %q", active.BlueprintPath, parent.BlueprintPath)
	}
	childManifest, err := reg.Get("implement", "1.0.0")
	testutil.FailErr(t, "Get implement manifest", err)
	childVars, err := wfStore.Runs.GetScaffoldVars(ctx, active.ID)
	testutil.FailErr(t, "Get child vars", err)
	childRequest, ok := runstate.RequestStateFromVars(childVars)
	if !ok || childRequest.Text != "test request" || childRequest.Source != "inherited" {
		t.Fatalf("child request = %+v ok=%v", childRequest, ok)
	}
	snapshot := mgr.Snapshots.Project(ctx, active, childManifest, childVars)
	if snapshot.BlueprintBody == "" || snapshot.Blueprint == nil || snapshot.Blueprint.Path != parent.BlueprintPath {
		t.Fatalf("child runtime Blueprint = %+v body=%q", snapshot.Blueprint, snapshot.BlueprintBody)
	}
	if snapshot.BlueprintApproval == nil || snapshot.BlueprintApproval.Status != "approved" || snapshot.BlueprintApproval.Origin != "inherited" || snapshot.BlueprintApproval.ParentRunID != parent.ID {
		t.Fatalf("child runtime Blueprint approval = %+v", snapshot.BlueprintApproval)
	}
	_, err = mgr.Asks.RequestUserInput(ctx, sess.ID, workflowinputs.UserInputRequest{
		Prompt:       "Approve the inherited Blueprint again?",
		ResponseType: workflowdef.FeedbackResponseSingleChoice,
		Options:      []string{"Approve", "Reject"},
	})
	reject := &workflowinputs.AskUserReject{}
	if !errors.As(err, &reject) || reject.Code != "ASK_USER_INHERITED_BLUEPRINT_FORBIDDEN" {
		t.Fatalf("ask_user under inherited Blueprint err = %v want ASK_USER_INHERITED_BLUEPRINT_FORBIDDEN", err)
	}
	if reject.Data["blueprint_status"] != "approved" || reject.Data["parent_run_id"] != parent.ID {
		t.Fatalf("ask_user reject data = %+v", reject.Data)
	}
	replayed, err := mgr.Children.InvokeChild(ctx, parent.ID, workflowdef.InvokeWorkflowSpec{
		WorkflowID: "implement", Version: "1.0.0", Blueprint: workflowdef.ChildBlueprintInherit,
	})
	testutil.FailErr(t, "replay InvokeChild", err)
	if replayed.ID != active.ID {
		t.Fatalf("replayed child id = %q want %q", replayed.ID, active.ID)
	}
	bound, err := blueprintMgr.Get(ctx, active.ProjectID, active.BlueprintPath)
	testutil.FailErr(t, "Get inherited Blueprint", err)
	changed := bound.Content + "\nApproval-invalidating edit.\n"
	_, err = blueprintMgr.Update(ctx, active.ProjectID, active.BlueprintPath, &changed, nil)
	testutil.FailErr(t, "mutate inherited Blueprint", err)
	snapshot = mgr.Snapshots.Project(ctx, active, childManifest, childVars)
	if snapshot.BlueprintApproval == nil || snapshot.BlueprintApproval.Status != "invalid" {
		t.Fatalf("mutated inherited Blueprint approval = %+v want invalid", snapshot.BlueprintApproval)
	}
}

func TestInvokeChildRejectsGrandchildDepth(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "depth.db")

	sessStore := store.NewSQL(sqlDB)
	wfStore := testRunStore(t, sqlDB)
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	mgr := NewManager(wfStore, sessStore, reg, nil)

	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create session", err)

	parentID := "parent-run"
	childID := "child-run"
	pid := parentID
	runParent := api.WorkflowRun{
		ID: parentID, SessionID: sess.ID, WorkflowID: "plan", WorkflowVersion: "1.0.0",
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "execute",
	}
	runChild := api.WorkflowRun{
		ID: childID, SessionID: sess.ID, WorkflowID: "implement", WorkflowVersion: "1.0.0",
		ParentRunID: &pid, Status: api.WorkflowRunStatusRunning, CurrentPhase: "orient",
	}
	testutil.FailErr(t, "Create parent", wfStore.State.CreateState(ctx, &runParent, "", nil))
	testutil.FailErr(t, "Create child", wfStore.State.CreateState(ctx, &runChild, "", nil))

	_, err = mgr.Children.InvokeChild(ctx, childID, workflowdef.InvokeWorkflowSpec{
		WorkflowID: "implement", Version: "1.0.0", Blueprint: workflowdef.ChildBlueprintNone,
	})
	if err == nil {
		t.Fatal("expected depth exceeded error")
	}
	if !errors.Is(err, ErrSubworkflowDepthExceeded) {
		t.Fatalf("err = %v want ErrSubworkflowDepthExceeded", err)
	}
}

func TestChildCompleteAutoFinishesParentPlan(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	deps := conditions.TestRegistryDepsWithEvidence()
	deps.DeliveryReported = func(context.Context, string, string, string) (bool, error) { return true, nil }
	setTestRegistry(t, mgr, blueprintMgr, deps)
	ctx := context.Background()

	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	run, err = advancePlanToApprovePhase(ctx, mgr, run)
	testutil.FailErr(t, "advancePlanToApprovePhase", err)
	run, err = mgr.Approvals.SyncHumanApproval(workflowCaller(t, mgr), run.ID, projectDir)
	testutil.FailErr(t, "SyncHumanApproval", err)
	parentID := run.ID

	child, err := mgr.Store.Runs.ActiveBySession(ctx, "sess-1")
	testutil.FailErr(t, "ActiveBySession", err)
	if child == nil {
		t.Fatal("expected implement child")
	}
	testutil.FailErr(t, "RecordBoardOrientReady", mgr.Fanout.RecordBoardOrientReady(ctx, child.SessionID, "test-orient"))
	testutil.FailErr(t, "RecordWorkerTerminalProof", mgr.Fanout.RecordWorkerTerminalProof(ctx, child.SessionID, "test-work", "complete"))
	child, err = mgr.Store.Runs.Get(ctx, child.ID)
	testutil.FailErr(t, "Get completed implement child", err)
	if child.Status != api.WorkflowRunStatusComplete {
		t.Fatalf("child status = %q phase=%q want complete", child.Status, child.CurrentPhase)
	}

	parent, err := mgr.Store.Runs.Get(ctx, parentID)
	testutil.FailErr(t, "Get parent", err)
	if parent.Status != api.WorkflowRunStatusComplete {
		t.Fatalf("parent status = %q want complete", parent.Status)
	}
	text, response, handled, err := mgr.Requests.PrepareUserRequest(ctx, "sess-1", "improve the graphics")
	testutil.FailErr(t, "prepare follow-up after plan completion", err)
	if text != "improve the graphics" || response != nil || handled {
		t.Fatalf("unexpected follow-up resolution: %q %+v %v", text, response, handled)
	}
	active, err := mgr.Store.Runs.ActiveBySession(ctx, "sess-1")
	testutil.FailErr(t, "read follow-up workflow", err)
	if active == nil || !runstate.IsAmbientRun(active) || active.ID == parent.ID || active.ID == child.ID {
		t.Fatalf("follow-up needs a fresh ambient run: %+v", active)
	}
	testutil.FailErr(t, "follow-up runnable", mgr.Policy.AssertSessionRunnable(ctx, "sess-1"))
	for _, id := range []string{parent.ID, child.ID, active.ID} {
		available, err := mgr.Presentation.ReportAvailable(ctx, id)
		testutil.FailErr(t, "report availability", err)
		if available {
			t.Fatalf("plan and implement must not expose documents: %s", id)
		}
	}
}

func TestExitChildRunResumesParent(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDepsWithEvidence())
	ctx := context.Background()

	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	run, err = advancePlanToApprovePhase(ctx, mgr, run)
	testutil.FailErr(t, "advancePlanToApprovePhase", err)
	run, err = mgr.Approvals.SyncHumanApproval(workflowCaller(t, mgr), run.ID, projectDir)
	testutil.FailErr(t, "SyncHumanApproval", err)
	parentID := run.ID
	child, err := mgr.Store.Runs.ActiveBySession(ctx, "sess-1")
	testutil.FailErr(t, "GetActive child", err)

	parent, err := mgr.Controls.Exit(ctx, "sess-1", child.ID, child.Revision, "user_exit")
	testutil.FailErr(t, "Exit child", err)
	if parent == nil || parent.ID != parentID {
		t.Fatalf("Exit leaf = %+v want parent %q", parent, parentID)
	}
	if parent.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("parent status = %q want running", parent.Status)
	}
	if parent.CurrentPhase != "execute" {
		t.Fatalf("parent phase = %q want execute", parent.CurrentPhase)
	}
}

func TestExitCatalogRunSpawnsAmbientImplement(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "catalog-exit.db")

	sessStore := store.NewSQL(sqlDB)
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	wfStore := testRunStore(t, sqlDB)
	mgr := NewManager(wfStore, sessStore, reg, nil)
	dir := t.TempDir()
	blueprintMgr := WireBlueprintDepsForTest(mgr, dir)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDeps())

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create session", err)

	run, err := mgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{WorkflowID: "plan", WorkflowVersion: "1.0.0", Request: "test request"})
	if err != nil {
		testutil.FailErr(t, "Start plan", err)
	}
	if _, err := mgr.Controls.Exit(ctx, sess.ID, run.ID, run.Revision, "user_exit"); err != nil {
		testutil.FailErr(t, "Exit catalog", err)
	}
	active, err := wfStore.Runs.ActiveBySession(ctx, sess.ID)
	testutil.FailErr(t, "ActiveBySession", err)
	if active == nil {
		t.Fatal("expected fresh ambient implement run")
	}
	if active.WorkflowID != "implement" {
		t.Fatalf("ambient workflow_id = %q want implement", active.WorkflowID)
	}
	if active.ParentRunID != nil {
		t.Fatalf("ambient run must not have parent_run_id")
	}
}

func TestChildManifestWithInvokeRejectedAtRuntime(t *testing.T) {
	m := workflowdef.Manifest{
		ID: "bad-child", Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:             "nest",
			InvokeWorkflow: &workflowdef.InvokeWorkflowSpec{WorkflowID: "implement", Version: "1.0.0"},
			CompleteWhen:   workflowdef.CompleteWhenGatesSatisfied,
			Gates:          []string{"child_run_complete"},
		}},
	}
	if err := workflowdef.ManifestAllowsAsChild(m); err == nil {
		t.Fatal("expected child manifest with invoke_workflow to be rejected")
	}
}
