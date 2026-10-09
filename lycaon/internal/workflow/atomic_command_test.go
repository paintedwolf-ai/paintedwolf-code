package workflow

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"slices"
	"testing"
)

func TestWorkflowPauseExactReplayReturnsReceiptWithoutDuplicateBoundary(t *testing.T) {
	mgr, sessions, _, _ := testManager(t)
	ctx := context.Background()
	run, err := mgr.Starts.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		OperationID: uuid.NewString(), WorkflowID: "plan", WorkflowVersion: "1.0.0", Request: "test request",
	})
	testutil.FailErr(t, "start workflow", err)
	expected := run.Revision
	commandCtx := runstate.WithExpectedRevision(ctx, expected)
	first, err := mgr.Controls.Pause(commandCtx, run.ID, "review")
	testutil.FailErr(t, "pause workflow", err)
	before, err := sessions.GetMessages(ctx, run.SessionID)
	testutil.FailErr(t, "list messages before replay", err)
	replayed, err := mgr.Controls.Pause(commandCtx, run.ID, "review")
	testutil.FailErr(t, "replay pause", err)
	after, err := sessions.GetMessages(ctx, run.SessionID)
	testutil.FailErr(t, "list messages after replay", err)
	if replayed.Revision != first.Revision || len(after) != len(before) {
		t.Fatalf("replay revision=%d want=%d messages=%d want=%d", replayed.Revision, first.Revision, len(after), len(before))
	}
	if _, err := mgr.Controls.Resume(commandCtx, run.ID); !errors.Is(err, runstate.ErrRevisionConflict) {
		t.Fatalf("competing command error = %v", err)
	}
}

func TestWorkflowAdvanceRejectionReplaysReceipt(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	ctx := context.Background()
	run, err := mgr.Starts.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		OperationID: uuid.NewString(), WorkflowID: "plan", WorkflowVersion: "1.0.0", Request: "test request",
	})
	testutil.FailErr(t, "start workflow", err)
	commandCtx := runstate.WithExpectedRevision(ctx, run.Revision)
	_, firstErr := mgr.Phases.Advance(commandCtx, run.ID)
	first, ok := runstate.IsPhaseGateUnmet(firstErr)
	if !ok {
		t.Fatalf("first advance error = %v", firstErr)
	}
	_, replayErr := mgr.Phases.Advance(commandCtx, run.ID)
	replayed, ok := runstate.IsPhaseGateUnmet(replayErr)
	if !ok {
		t.Fatalf("replayed advance error = %v", replayErr)
	}
	if replayed.Phase != first.Phase || replayed.FailedGate != first.FailedGate ||
		!slices.Equal(replayed.FailedLeaves, first.FailedLeaves) {
		t.Fatalf("replayed rejection = %#v want %#v", replayed, first)
	}
	if _, err := mgr.Controls.Pause(commandCtx, run.ID, "review"); !errors.Is(err, runstate.ErrRevisionConflict) {
		t.Fatalf("competing command error = %v", err)
	}
}

func TestWorkflowStartOperationReplaysOriginalRunAndRejectsRebinding(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	ctx := context.Background()
	opID := uuid.NewString()
	req := api.StartWorkflowRunRequest{OperationID: opID, WorkflowID: "plan", WorkflowVersion: "1.0.0"}
	first, err := mgr.Starts.StartHuman(ctx, "sess-1", req)
	testutil.FailErr(t, "start workflow", err)
	replayed, err := mgr.Starts.StartHuman(ctx, "sess-1", req)
	testutil.FailErr(t, "replay workflow start", err)
	if replayed.ID != first.ID {
		t.Fatalf("replayed run = %s want %s", replayed.ID, first.ID)
	}
	req.PresetID = "different"
	if _, err := mgr.Starts.StartHuman(ctx, "sess-1", req); err == nil {
		t.Fatal("expected workflow start idempotency conflict")
	}
}
