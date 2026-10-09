package lifecycle_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/hostctx"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestHumanStartSupersedesActiveNonAmbientRun(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()

	first, err := mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "first StartHuman", err)
	firstPlanID := first.BlueprintPath
	if firstPlanID == "" {
		t.Fatal("expected plan id on first run")
	}

	second, err := mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "second StartHuman supersede", err)
	if second.ID == first.ID {
		t.Fatal("expected a new run id after human supersede")
	}
	if second.BlueprintPath == "" || second.BlueprintPath == firstPlanID {
		t.Fatalf("expected a new governing plan id; first=%q second=%q", firstPlanID, second.BlueprintPath)
	}

	loaded, err := mgr.Store.Runs.Get(ctx, first.ID)
	testutil.FailErr(t, "Get first", err)
	if loaded.Status != api.WorkflowRunStatusCanceled {
		t.Fatalf("first status = %q want canceled", loaded.Status)
	}
	if mgr.Blueprints.Getter != nil {
		if p, err := mgr.Blueprints.Getter.Get(ctx, first.ProjectID, firstPlanID); err != nil || p == nil {
			t.Fatalf("prior blueprint file must remain after supersede: err=%v", err)
		}
	}
}

func TestHumanStartSupersedesAcrossWorkflows(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()

	first, err := mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman plan", err)

	second, err := mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman options supersede", err)
	if second.WorkflowID != "options" {
		t.Fatalf("workflow_id = %q want options", second.WorkflowID)
	}
	if second.ID == first.ID {
		t.Fatal("expected new run after cross-workflow human start")
	}
}

func TestFailedHumanStartLeavesActiveRunUntouched(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()

	active, err := mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman plan", err)

	_, err = mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "missing-workflow", WorkflowVersion: "1.0.0",
	})
	if !errors.Is(err, workflowdef.ErrUnknownWorkflow) {
		t.Fatalf("StartHuman missing workflow err = %v want ErrUnknownWorkflow", err)
	}
	loaded, err := mgr.Store.Runs.Get(ctx, active.ID)
	testutil.FailErr(t, "Get active", err)
	if loaded.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("failed replacement changed active status to %q", loaded.Status)
	}
}

func TestHumanReplacementRequiresReviewedActiveRevision(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	active, err := mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman plan", err)

	_, err = mgr.Starts.Start(hostctx.WithHumanWorkflowStart(ctx), sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0",
	})
	if !errors.Is(err, runstate.ErrWorkflowReplacementTargetRequired) {
		t.Fatalf("missing target error = %v, want runstate.ErrWorkflowReplacementTargetRequired", err)
	}
	_, err = mgr.Starts.Start(hostctx.WithHumanWorkflowStart(ctx), sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0",
		ReplaceRunID: active.ID, ExpectedRevision: active.Revision + 1,
	})
	if !errors.Is(err, runstate.ErrRevisionConflict) {
		t.Fatalf("stale target error = %v, want runstate.ErrRevisionConflict", err)
	}
	loaded, err := mgr.Store.Runs.Get(ctx, active.ID)
	testutil.FailErr(t, "Get active", err)
	if loaded.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("rejected replacement changed active status to %q", loaded.Status)
	}
}

func TestCoordinatorStartRequiresHumanActionWhenActive(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()

	_, err := mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)

	if _, err := mgr.Starts.Start(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0",
	}); !errors.Is(err, runstate.ErrWorkflowStartRequiresHumanApproval) {
		t.Fatalf("coordinator Start err = %v want runstate.ErrWorkflowStartRequiresHumanApproval", err)
	}
}

func TestPlanEmptyStartWaitsForWorkflowRequest(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := workflowCaller(t, mgr)
	run, err := mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)
	if run.CurrentPhase != "research" {
		t.Fatalf("phase = %q want research", run.CurrentPhase)
	}
	if _, err := mgr.Phases.Advance(ctx, run.ID); err == nil {
		t.Fatal("expected Advance to fail while the workflow request is pending")
	}
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if !runstate.FeedbackPending(vars, runstate.WorkflowRequestFeedbackID) || !runstate.RequestPending(vars) {
		t.Fatalf("workflow request not pending: %v", vars)
	}
	run, err = mgr.Feedback.ResolveUserFeedback(ctx, sessionID, run.ID, runstate.WorkflowRequestFeedbackID, "weather CLI")
	testutil.FailErr(t, "ResolveUserFeedback", err)
	vars, err = mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars resolved", err)
	request, ok := runstate.RequestStateFromVars(vars)
	if !ok || request.Text != "weather CLI" || request.Source != "answer" || request.Status != runstate.RequestStatusResolved {
		t.Fatalf("request = %+v ok=%v", request, ok)
	}
}

func TestPlanExplicitRequestStartsWithoutAsk(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	run, err := mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0", Request: "weather CLI",
	})
	testutil.FailErr(t, "StartHuman", err)
	if run.CurrentPhase != "research" {
		t.Fatalf("phase = %q want research", run.CurrentPhase)
	}
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	request, ok := runstate.RequestStateFromVars(vars)
	if !ok || request.Text != "weather CLI" || request.Source != "explicit" || request.Status != runstate.RequestStatusResolved {
		t.Fatalf("request = %+v ok=%v", request, ok)
	}
	if runstate.FeedbackPending(vars, runstate.WorkflowRequestFeedbackID) {
		t.Fatal("explicit request opened a workflow question")
	}
}
