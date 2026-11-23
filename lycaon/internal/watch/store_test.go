package watch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func openTestWatchStore(t *testing.T) (*Store, *FakeClock, db.Handle, Owner) {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "session-2", testdbseed.DefaultProjectID)
	clock := NewFakeClock(time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC))
	store := NewStore(database, clock)
	owner := Owner{ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1"}
	return store, clock, database, owner
}

func TestRegisterIdempotentRetryAndInitialProcessReconcile(t *testing.T) {
	store, _, _, owner := openTestWatchStore(t)
	store.SetProcessLookup(func(_ context.Context, sessionID, handle string) (ProcessCompletionFact, bool, error) {
		if sessionID == "session-1" && handle == "cmd-done" {
			return ProcessCompletionFact{
				OccurrenceID: "occ-done-1",
				Handle:       "cmd-done",
				Outcome:      "satisfied",
				Report:       "exit_code=0",
			}, true, nil
		}
		return ProcessCompletionFact{}, false, nil
	})

	req := RegisterRequest{
		Owner:        owner,
		InvocationID: "inv-1",
		Conditions:   []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-done"}}},
	}
	first, err := store.Register(t.Context(), req)
	testutil.FailErr(t, "register watch", err)
	if first.Handle != "watch-1" || first.State != "matched" || first.PendingMatches != 1 {
		t.Fatalf("unexpected first receipt: %+v", first)
	}
	if first.WaitCondition.Kind != ConditionWatchMatch || len(first.WaitCondition.Handles) != 1 || first.WaitCondition.Handles[0] != "watch-1" {
		t.Fatalf("unexpected wait condition: %+v", first.WaitCondition)
	}

	replayed, err := store.Register(t.Context(), req)
	testutil.FailErr(t, "replay register watch", err)
	if replayed.WatchID != first.WatchID || replayed.Handle != first.Handle {
		t.Fatalf("replay allocated new watch: got %+v, want %+v", replayed, first)
	}
}

func TestSelectOrParkImmediateParkAndTerminalReceiptReference(t *testing.T) {
	store, clock, _, owner := openTestWatchStore(t)

	rcpt, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-1"}}},
	})
	testutil.FailErr(t, "register watch", err)

	var woke []wokeLease
	store.SetLeaseWakeHandler(func(_ context.Context, lease awaitstore.Lease, winner awaitstore.Condition) {
		woke = append(woke, wokeLease{lease: lease, winner: winner})
	})

	parked, err := store.SelectOrPark(t.Context(), SelectRequest{
		Owner:     owner,
		Handles:   []string{rcpt.Handle},
		AllowPark: true,
		ParkLease: awaitstore.Lease{
			SessionID: owner.SessionID, ProjectID: owner.ProjectID,
			ToolCallID: "wait-call-1", ProfileID: "implement",
			Deadline:   clock.Now().Add(60 * time.Second),
			Conditions: []awaitstore.Condition{rcpt.WaitCondition},
			Reason:     "wait for cmd-1",
		},
	})
	testutil.FailErr(t, "park on watch", err)
	if !parked.Parked || parked.Lease.ID == "" {
		t.Fatalf("expected parked lease, got %+v", parked)
	}

	matches, err := store.RecordProcessDone(t.Context(), owner.SessionID, ProcessCompletionFact{
		OccurrenceID: "occ-cmd-1",
		Handle:       "cmd-1",
		Outcome:      "satisfied",
		Report:       "exit=0",
	})
	testutil.FailErr(t, "record process done", err)
	if len(matches) != 1 || len(woke) != 1 || woke[0].lease.ID != parked.Lease.ID {
		t.Fatalf("expected parked lease to settle once: matches=%d woke=%+v", len(matches), woke)
	}

	// Duplicate producer occurrence is ignored.
	dup, err := store.RecordProcessDone(t.Context(), owner.SessionID, ProcessCompletionFact{
		OccurrenceID: "occ-cmd-1",
		Handle:       "cmd-1",
		Outcome:      "satisfied",
	})
	testutil.FailErr(t, "record duplicate process done", err)
	if len(dup) != 0 {
		t.Fatalf("duplicate occurrence matched: %+v", dup)
	}

	// Selecting an already-delivered completed one-shot returns its terminal receipt reference immediately.
	late, err := store.SelectOrPark(t.Context(), SelectRequest{
		Owner:     owner,
		Handles:   []string{rcpt.Handle},
		AllowPark: true,
		ParkLease: awaitstore.Lease{
			SessionID: owner.SessionID, ProjectID: owner.ProjectID,
			ToolCallID: "wait-call-2", ProfileID: "implement",
			Deadline: clock.Now().Add(60 * time.Second),
		},
	})
	testutil.FailErr(t, "late select completed one-shot", err)
	if late.Parked || late.Terminal == nil || late.Terminal.Outcome != OutcomeCompleted || late.Terminal.ReceiptID == "" {
		t.Fatalf("expected terminal completed receipt ref, got %+v", late)
	}
}

func TestCrashRecoveryTieBreakingAndLegacyWaitsIntact(t *testing.T) {
	store, clock, database, owner := openTestWatchStore(t)
	dueTogether := clock.Now().Add(5 * time.Minute)

	w1, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		ExpiresAt:  dueTogether,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-a"}}},
	})
	testutil.FailErr(t, "register w1", err)
	w2, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		ExpiresAt:  clock.Now().Add(30 * time.Minute),
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-b"}}},
	})
	testutil.FailErr(t, "register w2", err)

	parked, err := store.SelectOrPark(t.Context(), SelectRequest{
		Owner:     owner,
		Handles:   []string{w1.Handle, w2.Handle},
		AllowPark: true,
		ParkLease: awaitstore.Lease{
			SessionID: owner.SessionID, ProjectID: owner.ProjectID,
			ToolCallID: "wait-tie", ProfileID: "implement",
			Deadline:   dueTogether,
			Conditions: []awaitstore.Condition{{Kind: ConditionWatchMatch, Handles: []string{w1.Handle, w2.Handle}}},
		},
	})
	testutil.FailErr(t, "park w1+w2", err)

	// Arm a direct-only wait in session-2 to prove recovery leaves it intact.
	waitStore := &awaitstore.Store{DB: database}
	directLease, err := waitStore.Arm(t.Context(), awaitstore.Lease{
		SessionID: "session-2", ProjectID: owner.ProjectID,
		ToolCallID: "direct-1", ProfileID: "implement",
		Deadline:   clock.Now().Add(20 * time.Minute),
		Conditions: []awaitstore.Condition{{Kind: "port_ready", Host: "localhost", Port: 8080}},
		Reason:     "direct wait",
	})
	testutil.FailErr(t, "arm direct wait", err)

	// Simulate restart after dueTogether: handle w1 expiry beats call timeout, and w2 remains active.
	clock.Set(dueTogether.Add(time.Second))
	restarted := NewStore(database, clock)
	var settled []wokeLease
	restarted.SetLeaseWakeHandler(func(_ context.Context, lease awaitstore.Lease, winner awaitstore.Condition) {
		settled = append(settled, wokeLease{lease: lease, winner: winner})
	})
	testutil.FailErr(t, "recover watch store", restarted.Recover(t.Context()))

	if len(settled) != 1 || settled[0].lease.ID != parked.Lease.ID {
		t.Fatalf("expected 1 settled lease, got %+v", settled)
	}
	if settled[0].winner.Kind != ConditionWatchMatch || settled[0].winner.Outcome != string(OutcomeExpired) {
		t.Fatalf("expected handle expiry to beat timeout, got %+v", settled[0].winner)
	}
	if !strings.Contains(settled[0].winner.Report, `"watch-2"`) {
		t.Fatalf("expected watch-2 reported still active in %s", settled[0].winner.Report)
	}

	activeDirect, ok, err := waitStore.ForSession(t.Context(), "session-2")
	testutil.FailErr(t, "check direct lease intact", err)
	if !ok || activeDirect.ID != directLease.ID {
		t.Fatalf("direct-only wait lease disturbed: ok=%v got=%+v", ok, activeDirect)
	}
}

func TestCancelIdempotencyInspectAndReplacementProtection(t *testing.T) {
	store, clock, _, owner := openTestWatchStore(t)

	rcpt, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-cancel"}}},
	})
	testutil.FailErr(t, "register watch", err)

	_, _, err = store.RecordMatch(t.Context(), rcpt.WatchID, MatchInput{
		SourceKind:   ConditionProcessDone,
		OccurrenceID: "occ-unclaimed",
		Outcome:      "satisfied",
		Evidence:     map[string]any{"exit_code": 0},
	}, nil)
	testutil.FailErr(t, "record match before cancel", err)

	// Inspect does not acknowledge pending matches.
	view, err := store.Inspect(t.Context(), owner, rcpt.Handle)
	testutil.FailErr(t, "inspect watch", err)
	if view.PendingMatches != 1 {
		t.Fatalf("expected 1 pending match, got %+v", view)
	}

	c1, err := store.Cancel(t.Context(), owner, rcpt.Handle)
	testutil.FailErr(t, "first cancel", err)
	c2, err := store.Cancel(t.Context(), owner, rcpt.Handle)
	testutil.FailErr(t, "repeated cancel", err)
	if c1.ReceiptID == "" || c1.ReceiptID != c2.ReceiptID || len(c1.UnclaimedMatches) != 1 {
		t.Fatalf("cancel receipts mismatch: c1=%+v c2=%+v", c1, c2)
	}

	// Unowned handle from session-2 is refused.
	otherOwner := Owner{ProjectID: owner.ProjectID, SessionID: "session-2"}
	_, err = store.Inspect(t.Context(), otherOwner, rcpt.Handle)
	var unknown *UnknownHandleError
	if !errors.As(err, &unknown) {
		t.Fatalf("expected UnknownHandleError for other session, got %v", err)
	}
	_ = clock
}
