package hitl_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// A join registered after the card settled is refused, so the answer covers
// only the actions that were held when the person gave it.
func TestJoinAfterDecisionIsNotPending(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)

	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID:      sessionID,
		Kind:           api.CheckpointKindToolApproval,
		Type:           hitl.DecisionTypeApprove,
		Title:          "Approve command",
		ProposedAction: &hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "echo hi"}},
	})
	testutil.FailErr(t, "mgr.RequestCheckpoint failed", err)

	testutil.FailErr(t, "join while pending", mgr.PatchPendingToolApprovalJoined(ctx, resp.CheckpointID, 2, []string{"tc-a", "tc-b"}, "", ""))
	approveCurrentOption(t, ctx, mgr, sessionID, resp.CheckpointID)
	err = mgr.PatchPendingToolApprovalJoined(ctx, resp.CheckpointID, 3, []string{"tc-a", "tc-b", "tc-c"}, "", "")
	if !errors.Is(err, hitl.ErrCheckpointNotPending) {
		t.Fatalf("join after decision err = %v, want ErrCheckpointNotPending", err)
	}
}
