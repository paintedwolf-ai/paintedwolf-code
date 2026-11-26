package pythonadmission

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLiveHeldApprovalExactOnceAndOneUseLease(t *testing.T) {
	clk := pythonruntime.NewFixedClock(time.Date(2026, 11, 26, 13, 0, 0, 0, time.UTC))
	tracker := pythonruntime.NewParkedGuestTracker(pythonruntime.ParkedGuestBudget{
		MaxParkedGuests:        2,
		MaxRetainedMemoryBytes: 64 * 1024 * 1024,
		MaxReviewLeases:        2,
	})
	leaseCreated := make(chan ApprovalLease, 1)
	broker := NewApprovalBroker(ApprovalBrokerConfig{
		ParkedTracker: tracker,
		Clock:         clk,
		OnHoldCreated: func(l ApprovalLease) {
			leaseCreated <- l
		},
	})

	store := NewMemoryEvidenceStore()
	recheckedTarget := pythonruntime.CanonicalTarget{
		Namespace:  pythonruntime.NamespaceWorkspace,
		RootID:     "root-1",
		Branch:     "main",
		RelPath:    "deploy/config.yaml",
		DocumentID: "doc-resolved-42",
	}
	var recheckCalls atomic.Int32

	obs := NewAdmissionObserver(ObserverConfig{
		ExecutionID: "exec-hold-1",
		CallID:      "call-hold-1",
		Actor:       "agent:coder",
		View:        "workspace:main",
		Surface:     "direct_python",
		Broker:      broker,
		Clock:       clk,
		Evidence:    store,
		Policy: func(_ context.Context, _ pythonruntime.ReachedOperation) PolicyDecision {
			return PolicyDecision{Kind: pythonruntime.DecisionHold, Reason: "requires human review"}
		},
		Recheck: func(_ context.Context, _ ApprovalLease) (pythonruntime.CanonicalTarget, error) {
			recheckCalls.Add(1)
			return recheckedTarget, nil
		},
		ReservationProvider: func(_ pythonruntime.ReachedOperation) pythonruntime.ParkedGuestReservation {
			return pythonruntime.ParkedGuestReservation{
				ExecutionID:         "exec-hold-1",
				RetainedMemoryBytes: 4 * 1024 * 1024,
				OpenDescriptors:     2,
				ReviewLeases:        1,
			}
		},
	})

	op := pythonruntime.ReachedOperation{
		OperationID: "op-deploy-1",
		Family:      pythonruntime.EffectFilesystemWrite,
		Operation:   "write",
		Target: pythonruntime.CanonicalTarget{
			Namespace: pythonruntime.NamespaceWorkspace,
			RootID:    "root-1",
			RelPath:   "deploy/config.yaml",
		},
		Args: map[string]any{"bytes": 128},
	}

	type admitRes struct {
		dec pythonruntime.OperationDecision
		err error
	}
	done := make(chan admitRes, 1)
	go func() {
		dec, err := obs.Admit(context.Background(), op)
		done <- admitRes{dec: dec, err: err}
	}()

	lease := <-leaseCreated
	if snap := tracker.Snapshot(); snap.ParkedGuests != 1 || snap.ReviewLeases != 1 {
		t.Fatalf("while parked snapshot = %+v, want 1 guest / 1 lease", snap)
	}

	testutil.FailErr(t, "approve live lease", broker.Approve(lease.LeaseID, "human approved"))

	res := <-done
	testutil.FailErr(t, "admit returned error", res.err)
	if res.dec.Kind != pythonruntime.DecisionAllow || res.dec.ApprovalLeaseID != lease.LeaseID {
		t.Fatalf("decision = %+v, want DecisionAllow with lease %s", res.dec, lease.LeaseID)
	}
	if recheckCalls.Load() != 1 {
		t.Fatalf("recheck called %d times, want 1", recheckCalls.Load())
	}

	// Parked reservation must be released immediately after settlement.
	if snap := tracker.Snapshot(); snap.ParkedGuests != 0 || snap.ReviewLeases != 0 {
		t.Fatalf("post-approval snapshot = %+v, want zero baseline", snap)
	}

	// Replaying the same one-use lease must be rejected!
	err := broker.Registry().Consume(
		lease.LeaseID,
		"exec-hold-1",
		"op-deploy-1",
		recheckedTarget,
		lease.ArgsDigest,
		clk.Now(),
	)
	if !errors.Is(err, ErrLeaseAlreadyConsumed) {
		t.Fatalf("second lease consume err = %v, want ErrLeaseAlreadyConsumed", err)
	}

	// Evidence record must carry the authoritative resolved target from recheck.
	recs := store.Admissions()
	if len(recs) != 1 || recs[0].AuthoritativeTarget.DocumentID != "doc-resolved-42" {
		t.Fatalf("unexpected admission record authoritative target: %+v", recs)
	}
}

func TestDeclineInsideLoopBlocksTryFinallyCleanupWrites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	latch := pythonruntime.NewStopLatch(cancel)
	tracker := pythonruntime.NewParkedGuestTracker(pythonruntime.ParkedGuestBudget{MaxParkedGuests: 4})
	leaseCh := make(chan ApprovalLease, 1)
	broker := NewApprovalBroker(ApprovalBrokerConfig{
		ParkedTracker: tracker,
		OnHoldCreated: func(l ApprovalLease) { leaseCh <- l },
	})
	store := NewMemoryEvidenceStore()

	obs := NewAdmissionObserver(ObserverConfig{
		ExecutionID: "exec-loop-decline",
		CallID:      "call-loop",
		StopLatch:   latch,
		Broker:      broker,
		Evidence:    store,
		Policy: func(_ context.Context, op pythonruntime.ReachedOperation) PolicyDecision {
			if op.OperationID == "op-2-held" {
				return PolicyDecision{Kind: pythonruntime.DecisionHold, Reason: "review op-2"}
			}
			return PolicyDecision{Kind: pythonruntime.DecisionAllow}
		},
	})

	// Step 1: earlier effect succeeds.
	dec1, err := obs.Admit(ctx, pythonruntime.ReachedOperation{
		OperationID: "op-1-ok",
		Family:      pythonruntime.EffectFilesystemWrite,
		Operation:   "write",
		Target:      pythonruntime.CanonicalTarget{Namespace: pythonruntime.NamespaceWorkspace, RelPath: "a.txt"},
	})
	testutil.FailErr(t, "admit op-1", err)
	if dec1.Kind != pythonruntime.DecisionAllow {
		t.Fatalf("op-1 decision = %s, want allow", dec1.Kind)
	}

	// Step 2: loop hits held op and human declines.
	dec2Ch := make(chan pythonruntime.OperationDecision, 1)
	go func() {
		d, _ := obs.Admit(ctx, pythonruntime.ReachedOperation{
			OperationID: "op-2-held",
			Family:      pythonruntime.EffectFilesystemWrite,
			Operation:   "write",
			Target:      pythonruntime.CanonicalTarget{Namespace: pythonruntime.NamespaceWorkspace, RelPath: "b.txt"},
		})
		dec2Ch <- d
	}()

	lease := <-leaseCh
	testutil.FailErr(t, "decline lease", broker.Decline(lease.LeaseID, "human declined"))
	dec2 := <-dec2Ch
	if dec2.Kind != pythonruntime.DecisionStop || dec2.Stop == nil || dec2.Stop.Code != pythonruntime.StopEffectNotAllowed {
		t.Fatalf("op-2 decision = %+v, want DecisionStop(COMMAND_EFFECT_NOT_ALLOWED)", dec2)
	}

	// Step 3: try/finally cleanup write in guest must be blocked immediately by latched stop.
	decCleanup, err := obs.Admit(context.Background(), pythonruntime.ReachedOperation{
		OperationID: "op-3-finally-cleanup",
		Family:      pythonruntime.EffectFilesystemWrite,
		Operation:   "write",
		Target:      pythonruntime.CanonicalTarget{Namespace: pythonruntime.NamespaceWorkspace, RelPath: "cleanup.tmp"},
	})
	testutil.FailErr(t, "admit finally cleanup", err)
	if decCleanup.Kind != pythonruntime.DecisionStop {
		t.Fatalf("finally cleanup decision = %+v, want DecisionStop", decCleanup)
	}
	if snap := tracker.Snapshot(); snap.ParkedGuests != 0 {
		t.Fatalf("parked tracker not zero after decline: %+v", snap)
	}

	// Verify commit cardinality from durable history: exactly 1 allowed op.
	allowedCount := 0
	for _, rec := range store.Admissions() {
		if rec.Decision == pythonruntime.DecisionAllow {
			allowedCount++
		}
	}
	if allowedCount != 1 {
		t.Fatalf("allowed commit count = %d, want 1", allowedCount)
	}
}

func TestHostRestartSettlesPendingApprovalAndRejectsStaleApproval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	latch := pythonruntime.NewStopLatch(cancel)
	var sealedBuf bytes.Buffer
	sw := pythonruntime.NewSealedWriter(&sealedBuf, latch, 0)
	_, err := sw.Write([]byte("partial-output-before-park\n"))
	testutil.FailErr(t, "write pre-park output", err)

	tracker := pythonruntime.NewParkedGuestTracker(pythonruntime.ParkedGuestBudget{MaxParkedGuests: 4})
	leaseCh := make(chan ApprovalLease, 1)
	broker := NewApprovalBroker(ApprovalBrokerConfig{
		ParkedTracker: tracker,
		OnHoldCreated: func(l ApprovalLease) { leaseCh <- l },
	})
	store := NewMemoryEvidenceStore()

	obs := NewAdmissionObserver(ObserverConfig{
		ExecutionID: "exec-restart-1",
		CallID:      "call-restart",
		StopLatch:   latch,
		Broker:      broker,
		Evidence:    store,
		Policy: func(_ context.Context, _ pythonruntime.ReachedOperation) PolicyDecision {
			return PolicyDecision{Kind: pythonruntime.DecisionHold, Reason: "awaiting review"}
		},
	})

	decCh := make(chan pythonruntime.OperationDecision, 1)
	go func() {
		d, _ := obs.Admit(ctx, pythonruntime.ReachedOperation{
			OperationID: "op-restart-held",
			Family:      pythonruntime.EffectFilesystemWrite,
			Operation:   "write",
			Target:      pythonruntime.CanonicalTarget{Namespace: pythonruntime.NamespaceWorkspace, RelPath: "out.txt"},
		})
		decCh <- d
	}()

	lease := <-leaseCh
	if snap := tracker.Snapshot(); snap.ParkedGuests != 1 {
		t.Fatalf("expected 1 parked guest, got %+v", snap)
	}

	// Simulate host restart while guest is parked.
	report := broker.SettleInterruptedOnRestart("host process restarted")
	if len(report.SettledLeaseIDs) != 1 || report.SettledLeaseIDs[0] != lease.LeaseID {
		t.Fatalf("unexpected restart settlement report: %+v", report)
	}
	if report.ReleasedParkedGuests != 1 || report.LatchedStopsTriggered != 1 {
		t.Fatalf("restart report counts mismatch: %+v", report)
	}

	dec := <-decCh
	if dec.Kind != pythonruntime.DecisionStop || dec.Stop == nil || dec.Stop.Code != pythonruntime.StopCancelled {
		t.Fatalf("post-restart decision = %+v, want DecisionStop(PYTHON_CANCELLED)", dec)
	}
	if !sw.Sealed() {
		t.Fatal("output stream was not sealed on host restart")
	}
	if sealedBuf.String() != "partial-output-before-park\n" {
		t.Fatalf("recovered sealed output = %q, want %q", sealedBuf.String(), "partial-output-before-park\n")
	}
	if snap := tracker.Snapshot(); snap.ParkedGuests != 0 {
		t.Fatalf("parked tracker not zero after restart: %+v", snap)
	}

	// Stale approval arriving after host restart MUST be rejected and never replay!
	staleErr := broker.Approve(lease.LeaseID, "late human click")
	if !errors.Is(staleErr, ErrStaleApprovalAfterRestart) {
		t.Fatalf("stale approval err = %v, want ErrStaleApprovalAfterRestart", staleErr)
	}
	finalLease, ok := broker.Registry().Get(lease.LeaseID)
	if !ok || finalLease.State != LeaseLostOnRestart {
		t.Fatalf("lease state after stale approval = %+v, want lost_on_restart", finalLease)
	}
}

func TestConcurrentParkedGuestsAndIndependentCancellation(t *testing.T) {
	tracker := pythonruntime.NewParkedGuestTracker(pythonruntime.ParkedGuestBudget{
		MaxParkedGuests: 3,
		MaxReviewLeases: 3,
	})
	leases := make(chan ApprovalLease, 4)
	broker := NewApprovalBroker(ApprovalBrokerConfig{
		ParkedTracker: tracker,
		OnHoldCreated: func(l ApprovalLease) { leases <- l },
	})

	var wg sync.WaitGroup
	results := make([]pythonruntime.OperationDecision, 3)
	ctxs := make([]context.Context, 3)
	cancels := make([]context.CancelFunc, 3)

	for i := range 3 {
		ctxs[i], cancels[i] = context.WithCancel(context.Background())
		defer cancels[i]()
		idx := i
		obs := NewAdmissionObserver(ObserverConfig{
			ExecutionID: fmt.Sprintf("exec-conc-%d", idx),
			Broker:      broker,
			Policy: func(_ context.Context, _ pythonruntime.ReachedOperation) PolicyDecision {
				return PolicyDecision{Kind: pythonruntime.DecisionHold}
			},
		})
		wg.Add(1)
		go func() {
			defer wg.Done()
			dec, _ := obs.Admit(ctxs[idx], pythonruntime.ReachedOperation{
				OperationID: fmt.Sprintf("op-conc-%d", idx),
				Family:      pythonruntime.EffectFilesystemWrite,
				Operation:   "write",
			})
			results[idx] = dec
		}()
	}

	byExec := make(map[string]ApprovalLease, 3)
	for range 3 {
		l := <-leases
		byExec[l.ExecutionID] = l
	}
	if snap := tracker.Snapshot(); snap.ParkedGuests != 3 {
		t.Fatalf("expected 3 concurrently parked guests, got %+v", snap)
	}

	// A 4th guest must be rejected immediately with StopWorkLimitExceeded due to parked budget.
	obs4 := NewAdmissionObserver(ObserverConfig{
		ExecutionID: "exec-conc-overflow",
		Broker:      broker,
		Policy: func(_ context.Context, _ pythonruntime.ReachedOperation) PolicyDecision {
			return PolicyDecision{Kind: pythonruntime.DecisionHold}
		},
	})
	dec4, err := obs4.Admit(context.Background(), pythonruntime.ReachedOperation{
		OperationID: "op-overflow",
		Family:      pythonruntime.EffectFilesystemWrite,
		Operation:   "write",
	})
	testutil.FailErr(t, "admit 4th parked guest", err)
	if dec4.Kind != pythonruntime.DecisionStop || dec4.Stop == nil || dec4.Stop.Code != pythonruntime.StopWorkLimitExceeded {
		t.Fatalf("4th guest decision = %+v, want PYTHON_WORK_LIMIT_EXCEEDED", dec4)
	}

	// Cancel guest 1 independently; guests 0 and 2 remain parked.
	cancels[1]()
	testutil.FailErr(t, "approve guest 0", broker.Approve(byExec["exec-conc-0"].LeaseID, "ok"))
	testutil.FailErr(t, "decline guest 2", broker.Decline(byExec["exec-conc-2"].LeaseID, "no"))

	wg.Wait()

	if results[0].Kind != pythonruntime.DecisionAllow {
		t.Fatalf("guest 0 decision = %+v, want allow", results[0])
	}
	if results[1].Kind != pythonruntime.DecisionStop || results[1].Stop == nil || results[1].Stop.Code != pythonruntime.StopCancelled {
		t.Fatalf("guest 1 decision = %+v, want stop(PYTHON_CANCELLED)", results[1])
	}
	if results[2].Kind != pythonruntime.DecisionStop || results[2].Stop == nil || results[2].Stop.Code != pythonruntime.StopEffectNotAllowed {
		t.Fatalf("guest 2 decision = %+v, want stop(COMMAND_EFFECT_NOT_ALLOWED)", results[2])
	}
	if snap := tracker.Snapshot(); snap != (pythonruntime.ParkedGuestSnapshot{}) {
		t.Fatalf("ParkedGuestTracker did not return to zero baseline: %+v", snap)
	}
}

func TestDurableRestartReconciliation(t *testing.T) {
	dir := t.TempDir()
	store1, err := NewFileContinuationStore(dir, "continuation.json")
	testutil.FailErr(t, "create file continuation store 1", err)

	clk := pythonruntime.NewFixedClock(time.Date(2026, 11, 26, 14, 0, 0, 0, time.UTC))
	leasePersisted := make(chan ApprovalLease, 1)
	broker1 := NewApprovalBroker(ApprovalBrokerConfig{
		Store: store1,
		Clock: clk,
		OnHoldCreated: func(l ApprovalLease) {
			leasePersisted <- l
		},
	})

	obs1 := NewAdmissionObserver(ObserverConfig{
		ExecutionID: "exec-durable-1",
		CallID:      "call-durable-1",
		Broker:      broker1,
		Clock:       clk,
		Policy: func(_ context.Context, _ pythonruntime.ReachedOperation) PolicyDecision {
			return PolicyDecision{Kind: pythonruntime.DecisionHold, Reason: "review before commit"}
		},
	})

	ctx1, cancel1 := context.WithCancel(context.Background())
	t.Cleanup(cancel1)

	go func() {
		_, _ = obs1.Admit(ctx1, pythonruntime.ReachedOperation{
			OperationID: "op-durable-write",
			Family:      pythonruntime.EffectFilesystemWrite,
			Operation:   "write",
			Target: pythonruntime.CanonicalTarget{
				Namespace: pythonruntime.NamespaceWorkspace,
				RootID:    "root-1",
				RelPath:   "important.txt",
			},
			Args: map[string]any{"bytes": 64},
		})
	}()

	lease := <-leasePersisted

	// Confirm the pending lease is durably persisted on disk before simulated crash.
	preSnap, err := store1.Load(context.Background())
	testutil.FailErr(t, "load pre-restart durable snapshot", err)
	if len(preSnap.Leases) != 1 || preSnap.Leases[0].LeaseID != lease.LeaseID || preSnap.Leases[0].State != LeasePending {
		t.Fatalf("pre-restart durable snapshot = %+v, want 1 pending lease", preSnap)
	}

	// Simulate host restart: discard broker1 completely and construct a brand-new
	// broker2 pointing at the same durable store on disk.
	store2, err := NewFileContinuationStore(dir, "continuation.json")
	testutil.FailErr(t, "create file continuation store 2", err)
	clk.Advance(30 * time.Second)
	broker2 := NewApprovalBroker(ApprovalBrokerConfig{
		Store: store2,
		Clock: clk,
	})

	report, err := broker2.ReconcileOnStartup(context.Background(), "host process restarted")
	testutil.FailErr(t, "reconcile broker2 on startup", err)
	if len(report.SettledLeaseIDs) != 1 || report.SettledLeaseIDs[0] != lease.LeaseID {
		t.Fatalf("reconcile report = %+v, want settled lease %s", report, lease.LeaseID)
	}

	// Verify durable history on disk records the settlement and lost_on_restart state.
	postSnap, err := store2.Load(context.Background())
	testutil.FailErr(t, "load post-restart durable snapshot", err)
	if len(postSnap.Settlements) != 1 || len(postSnap.Leases) != 1 || postSnap.Leases[0].State != LeaseLostOnRestart {
		t.Fatalf("post-restart durable snapshot = %+v, want 1 settlement and lost_on_restart lease", postSnap)
	}

	// Stale approval arriving on broker2 MUST be rejected with zero replay.
	staleErr := broker2.Approve(lease.LeaseID, "late human approval after restart")
	if !errors.Is(staleErr, ErrStaleApprovalAfterRestart) {
		t.Fatalf("broker2 stale approve err = %v, want ErrStaleApprovalAfterRestart", staleErr)
	}

	// Consuming the lease on broker2's registry MUST also be rejected.
	consumeErr := broker2.Registry().Consume(
		lease.LeaseID,
		lease.ExecutionID,
		lease.OperationID,
		lease.AuthoritativeTarget,
		lease.ArgsDigest,
		clk.Now(),
	)
	if !errors.Is(consumeErr, ErrLeaseLostOnRestart) {
		t.Fatalf("broker2 consume err = %v, want ErrLeaseLostOnRestart", consumeErr)
	}
}
