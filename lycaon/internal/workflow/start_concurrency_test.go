package workflow

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

// TestStartIsSerializedPerSession permits one concurrent start per session.
func TestStartIsSerializedPerSession(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)

	const N = 16
	var (
		wg           sync.WaitGroup
		successes    atomic.Int32
		conflicts    atomic.Int32
		other        atomic.Int32
		successRunID atomic.Value // string
	)
	wg.Add(N)
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			<-start
			run, err := mgr.Ambient.StartAmbient(context.Background(), sessionID, "plan", "1.0.0")
			switch {
			case err == nil && run != nil:
				successes.Add(1)
				successRunID.Store(run.ID)
			case errors.Is(err, runstate.ErrActiveRunExists):
				conflicts.Add(1)
			default:
				other.Add(1)
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := successes.Load(); got != 1 {
		t.Fatalf("concurrent Start: %d successes, want exactly 1 (conflicts=%d other=%d) — likely a TOCTOU race in Start", got, conflicts.Load(), other.Load())
	}
	if got := conflicts.Load(); got != N-1 {
		t.Fatalf("concurrent Start: %d runstate.ErrActiveRunExists, want %d (the losing N-1)", got, N-1)
	}

	// The winner remains the active run.
	active, err := mgr.Store.Runs.ActiveBySession(context.Background(), sessionID)
	testutil.FailErr(t, "mgr.Store.Runs.ActiveBySession failed", err)
	if active == nil {
		t.Fatal("no active run after winning Start")
	}
	if id, _ := successRunID.Load().(string); active.ID != id {
		t.Fatalf("active run id = %q, expected the success path id %q", active.ID, id)
	}
}

// TestTryAutoAdvanceIdempotentUnderConcurrency permits one advance per gate pass.
func TestTryAutoAdvanceIdempotentUnderConcurrency(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	run, err = mgr.Phases.Advance(ctx, run.ID)
	testutil.FailErr(t, "Advance research", err)
	startPhase := run.CurrentPhase
	if startPhase != "expand" {
		t.Fatalf("start phase = %q want expand", startPhase)
	}

	const N = 12
	var wg sync.WaitGroup
	results := make([]string, N)
	wg.Add(N)
	gate := make(chan struct{})
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			<-gate
			advanced, err := mgr.Phases.TryAutoAdvance(ctx, run.ID)
			if err != nil {
				results[i] = "err:" + err.Error()
				return
			}
			if advanced == nil {
				results[i] = "nil"
				return
			}
			results[i] = advanced.CurrentPhase
		}()
	}
	close(gate)
	wg.Wait()

	// Each caller observes the starting phase or its successor.
	for i, r := range results {
		if r == startPhase || r == "nil" {
			continue
		}
		// Completion may make a losing caller non-runnable.
		if len(r) >= 4 && r[:4] == "err:" {
			t.Fatalf("goroutine %d got bare error from TryAutoAdvance: %s", i, r)
		}
	}

	// At least one caller advances the valid plan.
	final, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "mgr.Store.Runs.Get failed", err)
	if final.CurrentPhase == startPhase {
		t.Fatalf("expected at least one auto-advance to land; still on %q", startPhase)
	}
}
