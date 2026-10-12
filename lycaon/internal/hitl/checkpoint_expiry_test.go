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
		SessionID: sessionID,
		Kind:      api.CheckpointKindToolApproval,
		Type:      hitl.DecisionTypeApprove,
		Title:     "Approve command",
		ProposedAction: &hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{
				Tool: "command",
				Args: map[string]any{"command": "echo hi"},
			},
		},
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
		if denied {
			t.Fatal("expiry must not suppress a new approval review")
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
		SessionID: sessionID,
		Kind:      api.CheckpointKindToolApproval,
		Type:      hitl.DecisionTypeApprove,
		Title:     "Approve command",
		ProposedAction: &hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{
				Tool: "command",
				Args: map[string]any{"command": "echo hi"},
			},
		},
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
		ProposedAction: &hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{
				Tool: "command",
				Args: map[string]any{"command": "echo old"},
			},
		},
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
		ProposedAction: &hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{
				Tool: "command",
				Args: map[string]any{"command": "echo restored"},
			},
		},
		CoalesceChat: sessionID, CoalesceGrantKey: "restored-key",
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

func TestStoppedCheckpointsPreservePendingRowsForRestart(t *testing.T) {
	database, original, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, t.Context(), database)
	insertSession(t, database, sessionID)
	original.SetCheckpointExpiry(func() time.Duration { return time.Hour })
	response, err := requestExplicitApprovalCheckpoint(t, ctx, original, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{
				Tool: "command",
				Args: map[string]any{"command": "echo restart"},
			},
		},
	})
	testutil.FailErr(t, "request pending approval", err)
	testutil.FailErr(t, "stop original checkpoint expiries", original.StopExpiryTimers(ctx))
	pending, err := original.PollCheckpoint(ctx, response.CheckpointID)
	testutil.FailErr(t, "read after shutdown", err)
	if pending.Status != hitl.DecisionStatusPending {
		t.Fatalf("shutdown status=%s, want pending", pending.Status)
	}
	_, err = database.ExecContext(ctx, "UPDATE checkpoints SET created_at = ? WHERE id = ?", db.FormatTime(time.Now().UTC().Add(-2*time.Hour)), response.CheckpointID)
	testutil.FailErr(t, "age original deadline", err)
	restarted := hitl.NewCheckpoints(hitl.NewSQLStore(database), nil, authzcontext.SQLRecorder(database))
	t.Cleanup(func() {
		testutil.FailErr(t, "stop restored checkpoint expiries", restarted.StopExpiryTimers(context.WithoutCancel(ctx)))
	})
	restarted.SetCheckpointExpiry(func() time.Duration { return time.Hour })
	testutil.FailErr(t, "restore approval after shutdown", restarted.RestorePending(ctx))
	settled, err := restarted.PollCheckpoint(ctx, response.CheckpointID)
	testutil.FailErr(t, "read restored expiry", err)
	if settled.Status != hitl.DecisionStatusExpired {
		t.Fatalf("restored status=%s, want expired from original deadline", settled.Status)
	}
}

func TestApprovalReviewIntentSurvivesRestartAndRejectsLateDenials(t *testing.T) {
	for _, renewal := range []string{"message", "continuation", "redirect"} {
		t.Run(renewal, func(t *testing.T) {
			database, checkpoints, chat := newTestManager(t)
			insertSession(t, database, chat)
			ctx := testdbseed.OwnerCaller(t, t.Context(), database)
			checkpoints.SetCheckpointExpiry(func() time.Duration { return 0 })
			request := func(session, key string) string {
				t.Helper()
				response, err := requestExplicitApprovalCheckpoint(t, ctx, checkpoints, hitl.CheckpointRequest{
					SessionID: session, Kind: api.CheckpointKindToolApproval, ReviewKeys: []string{key},
					ProposedAction: &hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "command", Args: map[string]any{"command": "fixture"}}},
				})
				testutil.FailErr(t, "request review", err)
				return response.CheckpointID
			}
			reject := func(session, checkpoint, direction string) {
				t.Helper()
				_, err := checkpoints.ResolveCheckpoint(ctx, session, checkpoint, api.CheckpointKindToolApproval,
					&hitl.DecisionResult{Comments: direction}, nil)
				testutil.FailErr(t, "reject review", err)
			}
			assertSuppressed := func(manager *hitl.Checkpoints, session, key string, want bool) {
				t.Helper()
				got, err := manager.ApprovalReviewSuppressed(ctx, session, key)
				testutil.FailErr(t, "read suppression", err)
				if got != want {
					t.Fatalf("suppression for %s/%s = %v, want %v", session, key, got, want)
				}
			}
			for _, child := range []string{"worker-a", "worker-b"} {
				testdbseed.InsertSession(t, database, child, testdbseed.DefaultProjectID)
				_, err := database.ExecContext(ctx, "UPDATE sessions SET parent_session_id = ? WHERE id = ?", chat, child)
				testutil.FailErr(t, "attach worker", err)
			}
			beforeIntent, err := checkpoints.ApprovalReviewIntent(ctx, chat)
			testutil.FailErr(t, "read initial intent", err)
			workerIntent, err := checkpoints.ApprovalReviewIntent(ctx, "worker-a")
			testutil.FailErr(t, "read worker intent", err)
			if workerIntent != beforeIntent {
				t.Fatal("worker intent differs from root")
			}
			const key = "sandbox:read_path:/protected"
			denied := request("worker-a", key)
			late := request("worker-a", "late-review")
			reject("worker-a", denied, "")
			assertSuppressed(checkpoints, "worker-b", key, true)
			assertSuppressed(checkpoints, chat, "other-path", false)
			restarted := hitl.NewCheckpoints(hitl.NewSQLStore(database), nil, authzcontext.SQLRecorder(database))
			assertSuppressed(restarted, chat, key, true)
			switch renewal {
			case "message", "continuation":
				insertUserIntentMessage(t, database, chat, "renewal", time.Now())
				if renewal == "continuation" {
					_, err := database.ExecContext(ctx, "UPDATE messages SET kind = 'user_continuation' WHERE id = 'renewal'")
					testutil.FailErr(t, "mark user continuation", err)
				}
			case "redirect":
				redirect := request("worker-b", "redirect")
				reject("worker-b", redirect, "Fix the request and try again.")
				assertSuppressed(checkpoints, chat, "redirect", false)
			}
			assertSuppressed(checkpoints, "worker-b", key, false)
			renewedIntent, err := checkpoints.ApprovalReviewIntent(ctx, chat)
			testutil.FailErr(t, "read renewed intent", err)
			if renewedIntent == beforeIntent {
				t.Fatal("fresh input did not renew intent")
			}
			reject("worker-a", late, "")
			assertSuppressed(checkpoints, chat, "late-review", false)
			restarted = hitl.NewCheckpoints(hitl.NewSQLStore(database), nil, authzcontext.SQLRecorder(database))
			assertSuppressed(restarted, "worker-b", key, false)
			assertSuppressed(restarted, chat, "late-review", false)
			restoredIntent, err := restarted.ApprovalReviewIntent(ctx, "worker-b")
			testutil.FailErr(t, "read restored intent", err)
			if restoredIntent != renewedIntent {
				t.Fatal("late denial or restart changed renewed intent")
			}
			newAttempt := request("worker-b", key)
			reject("worker-b", newAttempt, "")
			assertSuppressed(checkpoints, chat, key, true)
			testdbseed.InsertSession(t, database, "unrelated", testdbseed.DefaultProjectID)
			assertSuppressed(checkpoints, "unrelated", key, false)
		})
	}
}

func TestCanceledReviewDoesNotSuppressAnotherAsk(t *testing.T) {
	database, checkpoints, chat := newTestManager(t)
	insertSession(t, database, chat)
	ctx := testdbseed.OwnerCaller(t, t.Context(), database)
	response, err := requestExplicitApprovalCheckpoint(t, ctx, checkpoints, hitl.CheckpointRequest{
		SessionID: chat, Kind: api.CheckpointKindToolApproval, ReviewKeys: []string{"request"},
		ProposedAction: &hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "command"}},
	})
	testutil.FailErr(t, "request review", err)
	testutil.FailErr(t, "cancel review", checkpoints.CancelPendingForSession(ctx, chat, "stop"))
	final, err := checkpoints.PollCheckpoint(ctx, response.CheckpointID)
	testutil.FailErr(t, "read canceled review", err)
	if final.Status != hitl.DecisionStatusCanceled {
		t.Fatalf("status = %s", final.Status)
	}
	suppressed, err := checkpoints.ApprovalReviewSuppressed(ctx, chat, "request")
	testutil.FailErr(t, "read suppression", err)
	if suppressed {
		t.Fatal("cancellation suppressed a later review")
	}
}
