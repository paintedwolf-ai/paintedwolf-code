package blueprints_test

import (
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestSyncBlueprintTranscriptPreservesOrdOnPatch(t *testing.T) {
	mgr, sessStore, blueprintMgr, _ := testManagerWithRegistry(t)
	ctx := t.Context()
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

	testutil.FailErr(t, "SyncBlueprintTranscript append", mgr.Blueprints.SyncBlueprintTranscript(ctx, run.ProjectID, run.BlueprintPath, false))
	msgs, err := sessStore.GetMessages(ctx, run.SessionID)
	testutil.FailErr(t, "GetMessages after append", err)
	blueprintRow, ok := findBlueprintTranscriptMessage(msgs, run.BlueprintPath)
	if !ok {
		t.Fatal("expected blueprint transcript row")
	}
	ordBefore := blueprintRow.Ord

	body := "v2 body"
	current, err := blueprintMgr.Get(ctx, run.ProjectID, run.BlueprintPath)
	testutil.FailErr(t, "Get before patch", err)
	updated, err := blueprintMgr.Store.UpdateContent(ctx, run.ProjectID, run.BlueprintPath, body, blueprint.ContentDigest(current.Content))
	testutil.FailErr(t, "Update blueprint", err)
	testutil.FailErr(t, "SyncBlueprintTranscript patch", mgr.Blueprints.SyncBlueprintTranscript(ctx, run.ProjectID, run.BlueprintPath, true))

	msgs, err = sessStore.GetMessages(ctx, run.SessionID)
	testutil.FailErr(t, "GetMessages after patch", err)
	patched, ok := findBlueprintTranscriptMessage(msgs, run.BlueprintPath)
	if !ok {
		t.Fatal("expected blueprint transcript row after patch")
	}
	if patched.ID != blueprintRow.ID {
		t.Fatalf("blueprint row id changed: %q -> %q", blueprintRow.ID, patched.ID)
	}
	if patched.Ord != ordBefore {
		t.Fatalf("ord changed: %d -> %d", ordBefore, patched.Ord)
	}
	if patched.Content != updated.Content {
		t.Fatalf("content = %q want %q", patched.Content, updated.Content)
	}
	if patched.Blueprint == nil || patched.Blueprint.Status != api.BlueprintTranscriptStatusRevised {
		t.Fatalf("blueprint meta status = %+v", patched.Blueprint)
	}
}

func TestTriggerPhaseEnterSyncsBlueprintApprovalTranscriptForAnyPhaseName(t *testing.T) {
	mgr, _, _, projectDir := testManagerWithRegistry(t)
	ctx := t.Context()
	run, err := mgr.Starts.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman options", err)

	manifest, err := mgr.Resolver.ForRun(ctx, run)
	testutil.FailErr(t, "manifestForRun", err)
	approvalPhase, ok := manifest.PhaseByID("select")
	if !ok || approvalPhase.HumanApproval == nil {
		t.Fatal("options select must be a human approval phase")
	}
	run.CurrentPhase = approvalPhase.ID
	testutil.FailErr(t, "Store.Update select phase", mgr.Store.State.Update(ctx, run))
	vars := runstate.SetHumanApprovalReady(runstate.StampHumanApprovalPhase(nil, approvalPhase.HumanApproval, run.BlueprintPath), true)
	testutil.FailErr(t, "Store.UpdateVars approval ready", mgr.Store.State.UpdateVars(ctx, run, projectDir, vars))

	mgr.Phases.Entries.Trigger(ctx, run, projectDir, approvalPhase)
	msgs, err := mgr.Verdicts.Sessions.GetMessages(ctx, run.SessionID)
	testutil.FailErr(t, "GetMessages", err)
	blueprintRow, found := findBlueprintTranscriptMessage(msgs, run.BlueprintPath)
	if !found || blueprintRow.Blueprint == nil {
		t.Fatal("expected options approval transcript row")
	}
	if blueprintRow.Blueprint.Status != api.BlueprintTranscriptStatusAwaitingApproval ||
		!blueprintRow.Blueprint.CanApprove || !blueprintRow.Blueprint.ShowActions {
		t.Fatalf("approval transcript meta = %+v", blueprintRow.Blueprint)
	}
}

// A terminal run leaves a decision record without an active approval.

func findBlueprintTranscriptMessage(messages []api.Message, path string) (api.Message, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if api.IsBlueprintMessage(message) && message.Blueprint != nil && message.Blueprint.BlueprintPath == path {
			return message, true
		}
	}
	return api.Message{}, false
}
