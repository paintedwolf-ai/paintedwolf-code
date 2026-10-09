package hitl_test

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCheckpointAutoExpiresToDenied(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	mgr.SetCheckpointExpiry(func() time.Duration { return 15 * time.Millisecond })

	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID:      sessionID,
		Kind:           api.CheckpointKindToolApproval,
		Type:           hitl.DecisionTypeApprove,
		Title:          "Approve command",
		ProposedAction: &hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "echo hi"}},
	})
	testutil.FailErr(t, "mgr.RequestCheckpoint failed", err)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		final, perr := mgr.PollCheckpoint(ctx, resp.CheckpointID)
		testutil.FailErr(t, "PollCheckpoint", perr)
		if final.Status == hitl.DecisionStatusPending {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if final.Status != hitl.DecisionStatusExpired {
			t.Fatalf("status = %q, want expired (fail-safe deny)", final.Status)
		}
		denied, derr := mgr.SessionApprovalDenied(ctx, sessionID)
		testutil.FailErr(t, "SessionApprovalDenied", derr)
		if !denied {
			t.Fatal("expired checkpoint must count as a denied approval")
		}
		return
	}
	t.Fatal("pending checkpoint never expired — timeout not wired")
}

func TestCheckpointExpiryNotifiesToolApprovalTerminal(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	mgr.SetCheckpointExpiry(func() time.Duration { return 15 * time.Millisecond })

	var gotChat, gotKey string
	var gotStatus hitl.DecisionStatus
	var calls int
	mgr.Authority.SetToolApprovalTerminalHook(func(chat, key string, status hitl.DecisionStatus) {
		calls++
		gotChat, gotKey, gotStatus = chat, key, status
	})

	const grantKey = "command\x00\x00{\"command\":\"echo hi\"}"
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID:        sessionID,
		Kind:             api.CheckpointKindToolApproval,
		Type:             hitl.DecisionTypeApprove,
		Title:            "Approve command",
		ProposedAction:   &hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "echo hi"}},
		JoinedCount:      1,
		CoalesceChat:     sessionID,
		CoalesceGrantKey: grantKey,
	})
	testutil.FailErr(t, "RequestCheckpoint", err)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		final, perr := mgr.PollCheckpoint(ctx, resp.CheckpointID)
		testutil.FailErr(t, "PollCheckpoint", perr)
		if final.Status == hitl.DecisionStatusPending {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if final.Status != hitl.DecisionStatusExpired {
			t.Fatalf("status = %q want expired", final.Status)
		}
		if calls != 1 {
			t.Fatalf("terminal hook calls = %d want 1", calls)
		}
		if gotChat != sessionID || gotKey != grantKey {
			t.Fatalf("hook got chat=%q key=%q", gotChat, gotKey)
		}
		if gotStatus != hitl.DecisionStatusExpired {
			t.Fatalf("hook status = %q want expired", gotStatus)
		}
		return
	}
	t.Fatal("pending checkpoint never expired")
}

func TestRestorePendingExpiresFromOriginalCreationTime(t *testing.T) {
	ctx := context.Background()
	sqlDB, original, sessionID := newTestManager(t)
	insertSession(t, sqlDB, sessionID)
	original.SetCheckpointExpiry(func() time.Duration { return 0 })
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, original, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval,
		Type: hitl.DecisionTypeApprove, Title: "Old approval",
		ProposedAction: &hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "echo old"}},
	})
	testutil.FailErr(t, "create pending checkpoint", err)
	_, err = sqlDB.ExecContext(ctx, "UPDATE checkpoints SET created_at = ? WHERE id = ?", db.FormatTime(time.Now().UTC().Add(-time.Hour)), resp.CheckpointID)
	testutil.FailErr(t, "age pending checkpoint", err)

	restarted := hitl.NewCheckpoints(hitl.NewSQLStore(sqlDB), nil, authzcontext.SQLRecorder(sqlDB))
	restarted.SetCheckpointExpiry(func() time.Duration { return 15 * time.Minute })
	restored := 0
	restarted.Authority.SetToolApprovalRestoreHook(func(hitl.StoredCheckpoint) { restored++ })
	testutil.FailErr(t, "restore pending checkpoints", restarted.RestorePending(ctx))
	if restored != 0 {
		t.Fatalf("already-expired checkpoint restored into coalescing: %d", restored)
	}
	final, err := restarted.PollCheckpoint(ctx, resp.CheckpointID)
	testutil.FailErr(t, "poll restored checkpoint", err)
	if final.Status != hitl.DecisionStatusExpired {
		t.Fatalf("status = %q want expired", final.Status)
	}
}

func TestRestorePendingRebuildsCoalescingBeforeExpiry(t *testing.T) {
	ctx := context.Background()
	sqlDB, original, sessionID := newTestManager(t)
	insertSession(t, sqlDB, sessionID)
	original.SetCheckpointExpiry(func() time.Duration { return 0 })
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, original, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval,
		Type: hitl.DecisionTypeApprove, Title: "Restored approval",
		ProposedAction: &hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "echo restored"}},
		CoalesceChat:   sessionID, CoalesceGrantKey: "restored-key",
	})
	testutil.FailErr(t, "create pending checkpoint", err)

	restarted := hitl.NewCheckpoints(hitl.NewSQLStore(sqlDB), nil, authzcontext.SQLRecorder(sqlDB))
	restarted.SetCheckpointExpiry(func() time.Duration { return 2 * time.Second })
	order := make(chan string, 2)
	restarted.Authority.SetToolApprovalRestoreHook(func(row hitl.StoredCheckpoint) {
		if row.ID != resp.CheckpointID {
			t.Fatalf("restored checkpoint = %q", row.ID)
		}
		order <- "restore"
	})
	restarted.Authority.SetToolApprovalTerminalHook(func(_, _ string, _ hitl.DecisionStatus) { order <- "terminal" })
	testutil.FailErr(t, "restore pending checkpoints", restarted.RestorePending(ctx))
	if got := <-order; got != "restore" {
		t.Fatalf("first hook = %q want restore", got)
	}
	select {
	case got := <-order:
		if got != "terminal" {
			t.Fatalf("second hook = %q want terminal", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restored checkpoint did not expire")
	}
}

func TestRestoreRejectedToolApprovalDenials(t *testing.T) {
	sqlDB, original, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, original, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval,
		Type: hitl.DecisionTypeApprove, Title: "Denied approval",
		ProposedAction: &hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "echo denied"}},
		CoalesceChat:   sessionID, CoalesceGrantKey: "denied-key",
	})
	testutil.FailErr(t, "create checkpoint", err)
	_, err = original.ResolveCheckpoint(ctx, sessionID, resp.CheckpointID, api.CheckpointKindToolApproval,
		&hitl.DecisionResult{Approved: false}, nil)
	testutil.FailErr(t, "reject checkpoint", err)

	restarted := hitl.NewCheckpoints(hitl.NewSQLStore(sqlDB), nil, authzcontext.SQLRecorder(sqlDB))
	var restored []hitl.StoredCheckpoint
	restarted.Authority.SetToolApprovalDenyRestoreHook(func(row hitl.StoredCheckpoint) { restored = append(restored, row) })
	testutil.FailErr(t, "restore rejected tool approvals", restarted.Authority.RestoreRejectedToolApprovalDenials(ctx))
	if len(restored) != 1 || restored[0].ID != resp.CheckpointID {
		t.Fatalf("restored denials = %+v", restored)
	}

	// A visible user intent after the denial is the boundary that clears the
	// deny set, so a restart after it restores nothing for this chat.
	insertUserIntentMessage(t, sqlDB, sessionID, "msg-after-denial", time.Now().Add(time.Second))
	afterBoundary := hitl.NewCheckpoints(hitl.NewSQLStore(sqlDB), nil, authzcontext.SQLRecorder(sqlDB))
	restored = nil
	afterBoundary.Authority.SetToolApprovalDenyRestoreHook(func(row hitl.StoredCheckpoint) { restored = append(restored, row) })
	testutil.FailErr(t, "restore after intent boundary", afterBoundary.Authority.RestoreRejectedToolApprovalDenials(ctx))
	if len(restored) != 0 {
		t.Fatalf("denial restored across an intent boundary: %+v", restored)
	}
}

func TestStoppedCheckpointsPreservePendingRowsForRestart(t *testing.T) {
	database, original, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, t.Context(), database)
	insertSession(t, database, sessionID)
	original.SetCheckpointExpiry(func() time.Duration { return time.Hour })
	response, err := requestExplicitApprovalCheckpoint(t, ctx, original, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "echo restart"}},
	})
	testutil.FailErr(t, "request pending approval", err)
	original.StopExpiryTimers()
	pending, err := original.PollCheckpoint(ctx, response.CheckpointID)
	testutil.FailErr(t, "read after shutdown", err)
	if pending.Status != hitl.DecisionStatusPending {
		t.Fatalf("shutdown status=%s, want pending", pending.Status)
	}
	_, err = database.ExecContext(ctx, "UPDATE checkpoints SET created_at = ? WHERE id = ?", db.FormatTime(time.Now().UTC().Add(-2*time.Hour)), response.CheckpointID)
	testutil.FailErr(t, "age original deadline", err)
	restarted := hitl.NewCheckpoints(hitl.NewSQLStore(database), nil, authzcontext.SQLRecorder(database))
	t.Cleanup(restarted.StopExpiryTimers)
	restarted.SetCheckpointExpiry(func() time.Duration { return time.Hour })
	testutil.FailErr(t, "restore approval after shutdown", restarted.RestorePending(ctx))
	settled, err := restarted.PollCheckpoint(ctx, response.CheckpointID)
	testutil.FailErr(t, "read restored expiry", err)
	if settled.Status != hitl.DecisionStatusExpired {
		t.Fatalf("restored status=%s, want expired from original deadline", settled.Status)
	}
}
