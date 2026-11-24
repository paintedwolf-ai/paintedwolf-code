package watch_test

import (
	"context"
	"strings"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/watch"
)

func TestArmedLeaseWakeTimeoutRecoveryAndErrorFormatting(t *testing.T) {
	database := testdbfixture.Open(t, "settlement_cov.db")
	now := time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC)
	testdbseed.InsertSession(t, database, "sess-1", testdbseed.DefaultProjectID)

	clock := testutil.NewFakeClock(now)
	store := watch.NewStore(database, clock)
	var woke []awaitstore.Condition
	store.SetLeaseWakeHandler(func(_ context.Context, _ awaitstore.Lease, winner awaitstore.Condition) {
		woke = append(woke, winner)
	})

	owner := watch.Owner{ProjectID: testdbseed.DefaultProjectID, SessionID: "sess-1"}
	if owner.Destination() != "session" {
		t.Fatalf("session destination = %q, want session", owner.Destination())
	}
	jobOwner := watch.Owner{ProjectID: testdbseed.DefaultProjectID, SessionID: "sess-1", WorkerJobID: "job-1"}
	if jobOwner.Destination() != "job" {
		t.Fatalf("job destination = %q, want job", jobOwner.Destination())
	}

	// 1. Armed lease resolved when RecordProcessDone matches selected watch.
	w1, err := store.Register(t.Context(), watch.RegisterRequest{
		Owner:        owner,
		InvocationID: "inv-w1",
		Conditions:   []awaitstore.Condition{{Kind: "process_done", Handles: []string{"cmd-1"}}},
		ExpiresAt:    now.Add(10 * time.Minute),
	})
	testutil.FailErr(t, "register w1", err)
	park1, err := store.SelectOrPark(t.Context(), watch.SelectRequest{
		Owner:     owner,
		Handles:   []string{w1.Handle},
		AllowPark: true,
		ParkLease: awaitstore.Lease{
			ID:         "lease-1",
			SessionID:  "sess-1",
			ProjectID:  testdbseed.DefaultProjectID,
			ToolCallID: "tc-1",
			Deadline:   now.Add(5 * time.Minute),
			Conditions: []awaitstore.Condition{{Kind: watch.ConditionWatchMatch, Handles: []string{w1.Handle}}},
		},
	})
	testutil.FailErr(t, "park lease-1", err)
	if !park1.Parked {
		t.Fatal("expected lease-1 to park")
	}
	_, err = store.RecordProcessDone(t.Context(), "sess-1", watch.ProcessCompletionFact{
		OccurrenceID: "occ-cmd-1",
		Handle:       "cmd-1",
		Outcome:      "satisfied",
		Report:       "exit 0",
	})
	testutil.FailErr(t, "record cmd-1 done", err)
	if len(woke) != 1 || woke[0].Outcome != "matched" {
		t.Fatalf("woke after process_done = %+v, want 1 matched winner", woke)
	}

	// 2. Armed lease times out before watch expiry and reports active_handles; Recover rehydrates prior to due time.
	w2, err := store.Register(t.Context(), watch.RegisterRequest{
		Owner:        owner,
		InvocationID: "inv-w2",
		Conditions:   []awaitstore.Condition{{Kind: "process_done", Handles: []string{"cmd-2"}}},
		ExpiresAt:    now.Add(20 * time.Minute),
	})
	testutil.FailErr(t, "register w2", err)
	_, err = store.SelectOrPark(t.Context(), watch.SelectRequest{
		Owner:     owner,
		Handles:   []string{w2.Handle},
		AllowPark: true,
		ParkLease: awaitstore.Lease{
			ID:         "lease-2",
			SessionID:  "sess-1",
			ProjectID:  testdbseed.DefaultProjectID,
			ToolCallID: "tc-2",
			Deadline:   now.Add(2 * time.Minute),
			Conditions: []awaitstore.Condition{{Kind: watch.ConditionWatchMatch, Handles: []string{w2.Handle}}},
		},
	})
	testutil.FailErr(t, "park lease-2", err)

	testutil.FailErr(t, "recover before deadline", store.Recover(t.Context()))
	clock.Advance(3 * time.Minute)
	testutil.FailErr(t, "advance past lease-2 deadline", store.AdvanceDue(t.Context()))
	if len(woke) != 2 || woke[1].Outcome != "timed_out" || !strings.Contains(woke[1].Report, w2.Handle) {
		t.Fatalf("woke after timeout = %+v, want timed_out with active_handles", woke)
	}

	// 3. Armed lease settled by Cancel on w2.
	_, err = store.SelectOrPark(t.Context(), watch.SelectRequest{
		Owner:     owner,
		Handles:   []string{w2.Handle},
		AllowPark: true,
		ParkLease: awaitstore.Lease{
			ID:         "lease-3",
			SessionID:  "sess-1",
			ProjectID:  testdbseed.DefaultProjectID,
			ToolCallID: "tc-3",
			Deadline:   now.Add(15 * time.Minute),
			Conditions: []awaitstore.Condition{{Kind: watch.ConditionWatchMatch, Handles: []string{w2.Handle}}},
		},
	})
	testutil.FailErr(t, "park lease-3", err)
	_, err = store.Cancel(t.Context(), owner, w2.Handle)
	testutil.FailErr(t, "cancel w2", err)
	if len(woke) != 3 || woke[2].Outcome != string(watch.OutcomeCanceled) {
		t.Fatalf("woke after cancel = %+v, want canceled outcome", woke)
	}

	// 4. Typed error strings.
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&watch.CapacityError{LiveCount: 8, SlotCount: 8}, "live=8"},
		{&watch.ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "handles", Message: "empty"}, "WATCH_CONDITION_INVALID (handles): empty"},
		{&watch.ValidationError{Code: "WATCH_EXPIRY_INVALID", Message: "too soon"}, "WATCH_EXPIRY_INVALID: too soon"},
		{&watch.UnknownHandleError{Handle: "watch-99"}, "watch-99"},
		{&watch.TerminalError{Handle: "watch-1", Outcome: watch.OutcomeCanceled}, "watch-1 is terminal"},
	} {
		if !strings.Contains(tc.err.Error(), tc.want) {
			t.Fatalf("error %q missing %q", tc.err.Error(), tc.want)
		}
	}
}
