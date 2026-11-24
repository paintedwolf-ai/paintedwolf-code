package watch

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSessionTreeStopFencesAndCancelsExplicitDerivedAndChildWatches(t *testing.T) {
	store, _, database, owner := openTestWatchStore(t)
	childSessID := "child-sess-stop"
	workerJobID := "job-stop-1"
	testdbseed.InsertChildSession(t, database, childSessID, owner.SessionID)
	_, err := database.ExecContext(t.Context(), `INSERT INTO worker_jobs
		(id, project_id, child_session_id, agent_type, prompt, brief, created_at)
		VALUES (?, ?, ?, 'implementer', 'work', 'work', '2026-11-23T12:00:00Z')`,
		workerJobID, testdbseed.DefaultProjectID, childSessID)
	testutil.FailErr(t, "insert worker job", err)

	var wakeAdmissions atomic.Int32
	store.SetAmbientWakeGate(AmbientWakeGate{
		DispatchTurn: func(_ context.Context, _, _, _ string, onAdmitted func() error) error {
			if err := onAdmitted(); err != nil {
				return err
			}
			wakeAdmissions.Add(1)
			return nil
		},
	})

	var leaseWakes atomic.Int32
	store.SetLeaseWakeHandler(func(context.Context, awaitstore.Lease, awaitstore.Condition) {
		leaseWakes.Add(1)
	})

	// Earlier-turn explicit watch with one already-delivered match and one uncommitted reserved match.
	earlyRec, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Delivery:   DeliveryCollect,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone}},
	})
	testutil.FailErr(t, "register early watch", err)
	_, _, err = store.RecordMatch(t.Context(), earlyRec.WatchID, MatchInput{
		SourceKind: ConditionProcessDone, OccurrenceID: "early-delivered", Outcome: "satisfied",
		Evidence: map[string]any{"n": 1},
	}, nil)
	testutil.FailErr(t, "record delivered match", err)
	batch1, _, err := ReserveCollectBatch(t.Context(), store, owner, "req:committed")
	testutil.FailErr(t, "reserve committed batch", err)
	testutil.FailErr(t, "commit first batch", CommitCollectBatch(t.Context(), store, owner.SessionID, batch1.DeliveryID))

	_, _, err = store.RecordMatch(t.Context(), earlyRec.WatchID, MatchInput{
		SourceKind: ConditionProcessDone, OccurrenceID: "early-uncommitted", Outcome: "satisfied",
		Evidence: map[string]any{"n": 2},
	}, nil)
	testutil.FailErr(t, "record uncommitted match", err)
	_, _, err = ReserveCollectBatch(t.Context(), store, owner, "req:uncommitted")
	testutil.FailErr(t, "reserve uncommitted batch", err)

	// Derived Bash watch on a running command and explicit watch selected by an armed wait.
	derivedRec, err := store.Register(t.Context(), RegisterRequest{
		Owner:                owner,
		DerivedCommandHandle: "bg-running-1",
		Conditions:           []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"bg-running-1"}}},
	})
	testutil.FailErr(t, "register derived watch", err)

	selectedRec, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Delivery:   DeliveryWake,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"selected-cmd"}}},
	})
	testutil.FailErr(t, "register selected watch", err)
	_, err = store.SelectOrPark(t.Context(), SelectRequest{
		Owner:     owner,
		Handles:   []string{selectedRec.Handle},
		AllowPark: true,
		ParkLease: awaitstore.Lease{
			SessionID: owner.SessionID,
			ProjectID: owner.ProjectID,
			Deadline:  store.Clock().Now().Add(time.Minute),
		},
	})
	testutil.FailErr(t, "park wait on selected watch", err)

	// Child worker watch in the same session tree.
	childOwner := Owner{Kind: OwnerJob, ProjectID: owner.ProjectID, SessionID: childSessID, WorkerJobID: workerJobID}
	childRec, err := store.Register(t.Context(), RegisterRequest{
		Owner:      childOwner,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"worker-cmd"}}},
	})
	testutil.FailErr(t, "register child watch", err)

	// Execute session tree stop across root + child.
	summary, err := store.FenceAndCancelSessions(t.Context(), []string{owner.SessionID, childSessID})
	testutil.FailErr(t, "fence and cancel tree", err)
	if summary.CanceledCount != 4 {
		t.Fatalf("canceled count = %d, want 4 (%v)", summary.CanceledCount, summary.CanceledHandles)
	}
	if leaseWakes.Load() != 0 || wakeAdmissions.Load() != 0 {
		t.Fatalf("expected zero replacement turns during abort: leaseWakes=%d wakeAdmissions=%d", leaseWakes.Load(), wakeAdmissions.Load())
	}

	// Creation, producer callbacks, and ambient wakes are fenced while stopped.
	if _, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"after-stop"}}},
	}); !errors.Is(err, ErrSessionStopped) {
		t.Fatalf("expected ErrSessionStopped during stop fence, got %v", err)
	}
	if _, ok, err := store.RecordMatch(t.Context(), earlyRec.WatchID, MatchInput{
		SourceKind: ConditionProcessDone, OccurrenceID: "post-stop", Outcome: "satisfied",
		Evidence: map[string]any{"late": true},
	}, nil); err != nil || ok {
		t.Fatalf("producer callback bypassed stop fence: ok=%v err=%v", ok, err)
	}
	if woke, err := store.TriggerAmbientWake(t.Context(), owner.SessionID); err != nil || woke {
		t.Fatalf("ambient wake bypassed stop fence: woke=%v err=%v", woke, err)
	}

	// Committed delivery receipts and unclaimed evidence remain preserved in the final receipt.
	earlyView, err := store.Inspect(t.Context(), owner, earlyRec.Handle)
	testutil.FailErr(t, "inspect early canceled watch", err)
	if earlyView.State != StateTerminal || earlyView.Outcome != OutcomeCanceled || earlyView.LastDeliveryRef != batch1.DeliveryID {
		t.Fatalf("unexpected canceled watch state: %+v", earlyView)
	}
	childView, err := store.Inspect(t.Context(), childOwner, childRec.Handle)
	testutil.FailErr(t, "inspect child canceled watch", err)
	if childView.State != StateTerminal || childView.Outcome != OutcomeCanceled {
		t.Fatalf("child watch survived tree stop: %+v", childView)
	}
	derivedView, err := store.Inspect(t.Context(), owner, derivedRec.Handle)
	testutil.FailErr(t, "inspect derived canceled watch", err)
	if derivedView.State != StateTerminal || derivedView.Outcome != OutcomeCanceled {
		t.Fatalf("derived watch survived tree stop: %+v", derivedView)
	}

	// A later human turn clears the stop fence for fresh watches without reviving canceled ones.
	testutil.FailErr(t, "clear stop fence on human turn", store.ClearSessionStopFence(t.Context(), owner.SessionID))
	freshRec, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"fresh-cmd"}}},
	})
	testutil.FailErr(t, "register fresh watch after human turn", err)
	if freshRec.Handle == earlyRec.Handle {
		t.Fatalf("fresh watch reused canceled handle %s", freshRec.Handle)
	}
	earlyAfterHuman, err := store.Inspect(t.Context(), owner, earlyRec.Handle)
	testutil.FailErr(t, "re-inspect canceled watch after human turn", err)
	if earlyAfterHuman.State != StateTerminal || earlyAfterHuman.Outcome != OutcomeCanceled {
		t.Fatalf("human turn revived canceled watch: %+v", earlyAfterHuman)
	}
}

func TestHumanCancelSelectedWaitVsIdleWakeAndRestartStopReconcile(t *testing.T) {
	store, clock, database, owner := openTestWatchStore(t)

	var leaseWakes []string
	store.SetLeaseWakeHandler(func(_ context.Context, lease awaitstore.Lease, _ awaitstore.Condition) {
		leaseWakes = append(leaseWakes, lease.ID)
	})
	var ambientWakes int
	store.SetAmbientWakeGate(AmbientWakeGate{
		SessionEligible: func(context.Context, string) (bool, bool) { return false, true },
		DispatchTurn: func(_ context.Context, _, _, _ string, onAdmitted func() error) error {
			ambientWakes++
			return onAdmitted()
		},
	})

	// 1. Human canceling a watch selected by a parked turn settles and resumes that wait once.
	selRec, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"parked-cmd"}}},
	})
	testutil.FailErr(t, "register selected watch", err)
	parkedRes, err := store.SelectOrPark(t.Context(), SelectRequest{
		Owner:     owner,
		Handles:   []string{selRec.Handle},
		AllowPark: true,
		ParkLease: awaitstore.Lease{
			SessionID: owner.SessionID,
			ProjectID: owner.ProjectID,
			Deadline:  clock.Now().Add(time.Minute),
		},
	})
	testutil.FailErr(t, "park wait", err)
	_, err = store.Cancel(t.Context(), owner, selRec.Handle)
	testutil.FailErr(t, "human cancel selected watch", err)
	_, err = store.Cancel(t.Context(), owner, selRec.Handle)
	testutil.FailErr(t, "idempotent repeat cancel", err)
	if len(leaseWakes) != 1 || leaseWakes[0] != parkedRes.Lease.ID {
		t.Fatalf("expected parked wait settled exactly once, got %v", leaseWakes)
	}

	// 2. Canceling a wake-enabled watch in an idle chat withdraws its pending wake without starting a turn.
	wakeRec, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Delivery:   DeliveryWake,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"idle-wake-cmd"}}},
	})
	testutil.FailErr(t, "register idle wake watch", err)
	_, _, err = store.RecordMatch(t.Context(), wakeRec.WatchID, MatchInput{
		SourceKind: ConditionProcessDone, OccurrenceID: "occ-idle-wake", Outcome: "satisfied",
		Evidence: map[string]any{"ready": true},
	}, nil)
	testutil.FailErr(t, "record match before cancel", err)
	_, err = store.Cancel(t.Context(), owner, wakeRec.Handle)
	testutil.FailErr(t, "cancel wake watch in idle chat", err)
	store.SetAmbientWakeGate(AmbientWakeGate{
		SessionEligible: func(context.Context, string) (bool, bool) { return true, false },
		DispatchTurn: func(_ context.Context, _, _, _ string, onAdmitted func() error) error {
			ambientWakes++
			return onAdmitted()
		},
	})
	if woke, err := store.TriggerAmbientWake(t.Context(), owner.SessionID); err != nil || woke || ambientWakes != 0 {
		t.Fatalf("canceling wake watch started a turn: woke=%v count=%d err=%v", woke, ambientWakes, err)
	}

	// 3. Race registration, match, durable fence, and simulated crash before reconciliation; Recover finishes cleanup.
	raceOwner := Owner{ProjectID: owner.ProjectID, SessionID: "session-2"}
	raceRec, err := store.Register(t.Context(), RegisterRequest{
		Owner:      raceOwner,
		Delivery:   DeliveryWake,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone}},
	})
	testutil.FailErr(t, "register race watch", err)

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		_, _ = store.Register(t.Context(), RegisterRequest{
			Owner:      raceOwner,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"race-2"}}},
		})
	}()
	go func() {
		defer wg.Done()
		_, _, _ = store.RecordMatch(t.Context(), raceRec.WatchID, MatchInput{
			SourceKind: ConditionProcessDone, OccurrenceID: "race-occ", Outcome: "satisfied",
			Evidence: map[string]any{"raced": true},
		}, nil)
	}()
	go func() {
		defer wg.Done()
		_ = store.FenceSessions(t.Context(), []string{raceOwner.SessionID})
	}()
	wg.Wait()

	var postRestartWakes atomic.Int32
	restarted := NewStore(database, clock)
	restarted.SetAmbientWakeGate(AmbientWakeGate{
		DispatchTurn: func(_ context.Context, _, _, _ string, onAdmitted func() error) error {
			postRestartWakes.Add(1)
			return onAdmitted()
		},
	})
	testutil.FailErr(t, "recover stopped session on restart", restarted.Recover(t.Context()))
	if postRestartWakes.Load() != 0 {
		t.Fatalf("expected zero watch-originated turns after restart of stopped session, got %d", postRestartWakes.Load())
	}
	racedView, err := restarted.Inspect(t.Context(), raceOwner, raceRec.Handle)
	testutil.FailErr(t, "inspect reconciled watch after restart", err)
	if racedView.State != StateTerminal || racedView.Outcome != OutcomeCanceled {
		t.Fatalf("restart recovery left watch non-canceled: %+v", racedView)
	}
}
