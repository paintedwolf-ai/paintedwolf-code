package workflow

import (
	"testing"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSyncBlueprintTranscriptPreservesOrdOnPatch(t *testing.T) {
	mgr, sessStore, blueprintMgr, _ := testManagerWithRegistry(t)
	ctx := t.Context()
	run, err := mgr.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
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

	testutil.FailErr(t, "SyncBlueprintTranscript append", mgr.SyncBlueprintTranscript(ctx, run.ProjectID, run.BlueprintPath, false))
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
	testutil.FailErr(t, "SyncBlueprintTranscript patch", mgr.SyncBlueprintTranscript(ctx, run.ProjectID, run.BlueprintPath, true))

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

func TestBuildBlueprintTranscriptMetaAwaitingApproval(t *testing.T) {
	run := &api.WorkflowRun{CurrentPhase: "approve"}
	proposal := &api.Blueprint{
		Path:    settingsoverlay.Rel("blueprints/p1.md"),
		Title:   "Feature",
		Status:  api.BlueprintStatusDraft,
		Version: 3,
	}
	meta := buildBlueprintTranscriptMeta(run, proposal, true, false)
	if meta.Status != api.BlueprintTranscriptStatusAwaitingApproval {
		t.Fatalf("status = %q", meta.Status)
	}
	if meta.Phase != api.BlueprintCardPhaseReady || !meta.ShowActions {
		t.Fatalf("meta = %+v", meta)
	}
	if !meta.CanApprove {
		t.Fatal("CanApprove must follow awaiting approval only")
	}
}

func TestTriggerPhaseEnterSyncsBlueprintApprovalTranscriptForAnyPhaseName(t *testing.T) {
	mgr, _, _, projectDir := testManagerWithRegistry(t)
	ctx := t.Context()
	run, err := mgr.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman options", err)

	manifest, err := mgr.manifestForRun(ctx, run)
	testutil.FailErr(t, "manifestForRun", err)
	approvalPhase, ok := manifest.PhaseByID("select")
	if !ok || approvalPhase.HumanApproval == nil {
		t.Fatal("options select must be a human approval phase")
	}
	run.CurrentPhase = approvalPhase.ID
	testutil.FailErr(t, "Store.Update select phase", mgr.Store.Update(ctx, run))
	vars := SetHumanApprovalReady(StampHumanApprovalPhase(nil, approvalPhase.HumanApproval, run.BlueprintPath), true)
	testutil.FailErr(t, "Store.UpdateVars approval ready", mgr.Store.UpdateVars(ctx, run, projectDir, vars))

	mgr.triggerPhaseEnter(ctx, run, projectDir, approvalPhase)
	msgs, err := mgr.Sessions.GetMessages(ctx, run.SessionID)
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
func TestBuildBlueprintTranscriptMetaEndedRunRecordsTheDecision(t *testing.T) {
	proposal := &api.Blueprint{
		Path:    settingsoverlay.Rel("blueprints/p1.md"),
		Title:   "Feature",
		Status:  api.BlueprintStatusDraft,
		Version: 3,
	}

	rejected := buildBlueprintTranscriptMeta(
		&api.WorkflowRun{CurrentPhase: "approve", Status: api.WorkflowRunStatusCanceled},
		proposal, true, false,
	)
	if rejected.Status != api.BlueprintTranscriptStatusRejected {
		t.Fatalf("status = %q want rejected", rejected.Status)
	}
	if rejected.Phase != api.BlueprintCardPhaseRejected || rejected.PhaseLabel != "Rejected" {
		t.Fatalf("meta = %+v", rejected)
	}
	// Scaffold vars can still say the phase was awaiting when the run was cut.
	if rejected.CanApprove || rejected.ShowActions {
		t.Fatalf("ended run must not offer approval: %+v", rejected)
	}
	if !rejected.Collapsed {
		t.Fatal("a decided row collapses to its record")
	}

	superseded := buildBlueprintTranscriptMeta(
		&api.WorkflowRun{
			CurrentPhase: "approve",
			Status:       api.WorkflowRunStatusCanceled,
			PauseReason:  exitReasonSupersededByWorkflowStart,
		},
		proposal, true, false,
	)
	if superseded.Status != api.BlueprintTranscriptStatusSuperseded {
		t.Fatalf("status = %q want superseded", superseded.Status)
	}
	if superseded.PhaseLabel != "Superseded" {
		t.Fatalf("phase label = %q", superseded.PhaseLabel)
	}

	// An approved blueprint keeps its approval record whatever ended the run.
	approved := *proposal
	approved.Status = api.BlueprintStatusImplementing
	done := buildBlueprintTranscriptMeta(
		&api.WorkflowRun{CurrentPhase: "execute", Status: api.WorkflowRunStatusCanceled},
		&approved, false, false,
	)
	if done.Status != api.BlueprintTranscriptStatusApproved {
		t.Fatalf("status = %q want approved", done.Status)
	}
	if done.Phase != api.BlueprintCardPhaseBuilding {
		t.Fatalf("phase = %q", done.Phase)
	}
}
