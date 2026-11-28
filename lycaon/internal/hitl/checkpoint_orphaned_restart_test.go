package hitl_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRestorePendingCancelsOrphanedToolApprovalAndKeepsLiveOnes(t *testing.T) {
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "orphaned-approval.db")
	testdbseed.InsertSession(t, sqlDB, "sess-orphan", testdbseed.DefaultProjectID)
	caller := testdbseed.OwnerCaller(t, ctx, sqlDB)

	recorder := authzcontext.LedgerRecorder{Ledger: &authzcontext.Ledger{Store: authzcontext.NewSQLStore(sqlDB)}}
	store := hitl.NewSQLStore(sqlDB)
	now := time.Now().UTC()
	for _, row := range []hitl.StoredCheckpoint{
		{
			ID:        "cp-orphan",
			SessionID: "sess-orphan",
			ProjectID: testdbseed.DefaultProjectID,
			Kind:      api.CheckpointKindToolApproval,
			Status:    hitl.DecisionStatusPending,
			Type:      hitl.DecisionTypeApprove,
			Title:     "Orphaned approval",
			ToolName:  "command",
			CreatedAt: now,
			Payload:   map[string]any{"tool_call_id": "call-ended"},
		},
		{
			ID:        "cp-live",
			SessionID: "sess-orphan",
			ProjectID: testdbseed.DefaultProjectID,
			Kind:      api.CheckpointKindToolApproval,
			Status:    hitl.DecisionStatusPending,
			Type:      hitl.DecisionTypeApprove,
			Title:     "Live approval",
			ToolName:  "command",
			CreatedAt: now,
			Payload:   map[string]any{"tool_call_id": "call-live"},
		},
	} {
		testutil.FailErr(t, "persist pending checkpoint", store.Insert(ctx, row))
	}

	restarted := hitl.NewCheckpoints(hitl.NewSQLStore(sqlDB), nil, recorder)
	restarted.SetCheckpointExpiry(func() time.Duration { return 15 * time.Minute })
	restarted.SetInvocationTerminalCheck(func(_ context.Context, sessionID string, toolCallIDs []string) (bool, error) {
		if sessionID != "sess-orphan" {
			return false, nil
		}
		for _, id := range toolCallIDs {
			if id == "call-ended" {
				return true, nil
			}
		}
		return false, nil
	})
	testutil.FailErr(t, "restore pending checkpoints after restart", restarted.RestorePending(ctx))

	gotOrphan, err := restarted.PollCheckpoint(ctx, "cp-orphan")
	testutil.FailErr(t, "poll orphaned checkpoint", err)
	if gotOrphan.Status != hitl.DecisionStatusCanceled {
		t.Fatalf("expected orphaned checkpoint canceled, got %q", gotOrphan.Status)
	}
	if gotOrphan.ResolvedBy != authzledger.ResolvedByHostStop {
		t.Fatalf("expected ResolvedBy=%q, got %q", authzledger.ResolvedByHostStop, gotOrphan.ResolvedBy)
	}
	if gotOrphan.Result == nil || gotOrphan.Result.Approved || gotOrphan.Result.Comments == "" {
		t.Fatalf("expected actionable non-approved result on canceled orphan, got %+v", gotOrphan.Result)
	}

	// A stale click against the canceled orphan must be rejected without granting authority.
	_, err = restarted.ResolveCheckpoint(caller, "sess-orphan", "cp-orphan", api.CheckpointKindToolApproval, &hitl.DecisionResult{Approved: false, Comments: "stale"}, nil)
	if !errors.Is(err, hitl.ErrCheckpointNotPending) {
		t.Fatalf("expected ErrCheckpointNotPending on stale click, got %v", err)
	}

	// Unrelated valid pending approval stays pending.
	gotLive, err := restarted.PollCheckpoint(ctx, "cp-live")
	testutil.FailErr(t, "poll live checkpoint", err)
	if gotLive.Status != hitl.DecisionStatusPending {
		t.Fatalf("expected unrelated live checkpoint to remain pending, got %q", gotLive.Status)
	}
}
