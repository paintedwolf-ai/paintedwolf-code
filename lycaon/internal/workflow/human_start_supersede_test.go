package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/hostctx"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestHumanStartSupersedesActiveNonAmbientRun(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()

	first, err := mgr.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "first StartHuman", err)
	firstPlanID := first.BlueprintPath
	if firstPlanID == "" {
		t.Fatal("expected plan id on first run")
	}

	second, err := mgr.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "second StartHuman supersede", err)
	if second.ID == first.ID {
		t.Fatal("expected a new run id after human supersede")
	}
	if second.BlueprintPath == "" || second.BlueprintPath == firstPlanID {
		t.Fatalf("expected a new governing plan id; first=%q second=%q", firstPlanID, second.BlueprintPath)
	}

	loaded, err := mgr.Get(ctx, first.ID)
	testutil.FailErr(t, "Get first", err)
	if loaded.Status != api.WorkflowRunStatusCanceled {
		t.Fatalf("first status = %q want canceled", loaded.Status)
	}
	if mgr.BlueprintGet != nil {
		if p, err := mgr.BlueprintGet.Get(ctx, first.ProjectID, firstPlanID); err != nil || p == nil {
			t.Fatalf("prior blueprint file must remain after supersede: err=%v", err)
		}
	}
}

func TestHumanStartSupersedesAcrossWorkflows(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()

	first, err := mgr.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman plan", err)

	second, err := mgr.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
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

	active, err := mgr.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman plan", err)

	_, err = mgr.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "missing-workflow", WorkflowVersion: "1.0.0",
	})
	if !errors.Is(err, workflowdef.ErrUnknownWorkflow) {
		t.Fatalf("StartHuman missing workflow err = %v want ErrUnknownWorkflow", err)
	}
	loaded, err := mgr.Get(ctx, active.ID)
	testutil.FailErr(t, "Get active", err)
	if loaded.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("failed replacement changed active status to %q", loaded.Status)
	}
}

func TestHumanReplacementRequiresReviewedActiveRevision(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	active, err := mgr.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman plan", err)

	_, err = mgr.Start(hostctx.WithHumanWorkflowStart(ctx), sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0",
	})
	if !errors.Is(err, ErrWorkflowReplacementTargetRequired) {
		t.Fatalf("missing target error = %v, want ErrWorkflowReplacementTargetRequired", err)
	}
	_, err = mgr.Start(hostctx.WithHumanWorkflowStart(ctx), sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0",
		ReplaceRunID: active.ID, ExpectedRevision: active.Revision + 1,
	})
	if !errors.Is(err, ErrRunRevisionConflict) {
		t.Fatalf("stale target error = %v, want ErrRunRevisionConflict", err)
	}
	loaded, err := mgr.Get(ctx, active.ID)
	testutil.FailErr(t, "Get active", err)
	if loaded.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("rejected replacement changed active status to %q", loaded.Status)
	}
}

func TestCoordinatorStartRequiresHumanActionWhenActive(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()

	_, err := mgr.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)

	if _, err := mgr.Start(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0",
	}); !errors.Is(err, ErrWorkflowStartRequiresHumanApproval) {
		t.Fatalf("coordinator Start err = %v want ErrWorkflowStartRequiresHumanApproval", err)
	}
}

func TestPlanEmptyStartWaitsForWorkflowRequest(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := workflowCaller(t, mgr)
	run, err := mgr.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)
	if run.CurrentPhase != "research" {
		t.Fatalf("phase = %q want research", run.CurrentPhase)
	}
	if _, err := mgr.Advance(ctx, run.ID); err == nil {
		t.Fatal("expected Advance to fail while the workflow request is pending")
	}
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if !feedbackPending(vars, workflowRequestFeedbackID) || !requestPending(vars) {
		t.Fatalf("workflow request not pending: %v", vars)
	}
	run, err = mgr.ResolveUserFeedback(ctx, sessionID, run.ID, workflowRequestFeedbackID, "weather CLI")
	testutil.FailErr(t, "ResolveUserFeedback", err)
	vars, err = mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars resolved", err)
	request, ok := requestStateFromVars(vars)
	if !ok || request.Text != "weather CLI" || request.Source != "answer" || request.Status != requestStatusResolved {
		t.Fatalf("request = %+v ok=%v", request, ok)
	}
}

func TestPlanExplicitRequestStartsWithoutAsk(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	run, err := mgr.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0", Request: "weather CLI",
	})
	testutil.FailErr(t, "StartHuman", err)
	if run.CurrentPhase != "research" {
		t.Fatalf("phase = %q want research", run.CurrentPhase)
	}
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	request, ok := requestStateFromVars(vars)
	if !ok || request.Text != "weather CLI" || request.Source != "explicit" || request.Status != requestStatusResolved {
		t.Fatalf("request = %+v ok=%v", request, ok)
	}
	if feedbackPending(vars, workflowRequestFeedbackID) {
		t.Fatal("explicit request opened a workflow question")
	}
}
