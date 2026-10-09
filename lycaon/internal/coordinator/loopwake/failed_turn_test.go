package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/testutil"
	"sync/atomic"
	"testing"
	"time"
)

func TestFailedTurnHoldsQueuedPhaseWakeUntilExplicitExecution(t *testing.T) {
	f := newQueuedWaitFixture(t)
	var blocked atomic.Bool
	f.deps.HostTurnBlocked = func(context.Context, string) bool { return blocked.Load() }
	f.loop.SetDeps(f.deps)
	finish := f.loop.Admission.BeginPromptExecution(t.Context(), "session")
	f.loop.Nudges.Nudge(t.Context(), "session", anchor.PhaseAdvanced, "", "", anchor.Envelope{})
	blocked.Store(true)
	f.drain(t, finish)
	f.loop.Nudges.DrainPending(t.Context(), "session")
	if f.prompts.Load() != 0 || !f.loop.Nudges.HasPendingLoopWakes("session") {
		t.Fatalf("prompts=%d, pending=%v", f.prompts.Load(), f.loop.Nudges.HasPendingLoopWakes("session"))
	}
	release, ok := f.loop.Admission.BeginUserTurnSettlement(t.Context(), "session")
	if !ok {
		t.Fatal("held wake prevented failed-turn settlement")
	}
	release()
	blocked.Store(false)
	f.loop.Nudges.DrainPending(t.Context(), "session")
	f.loop.Turns.WaitForAsyncTurns(testutil.BoundedContext(t, 2*time.Second))
	if f.prompts.Load() != 1 {
		t.Fatalf("resumed prompts=%d", f.prompts.Load())
	}
}

func TestFailedTurnPreservesUndeliveredWaitResultWithoutRetry(t *testing.T) {
	f := newQueuedWaitFixture(t)
	var blocked atomic.Bool
	f.deps.HostTurnBlocked = func(context.Context, string) bool { return blocked.Load() }
	var resumes atomic.Int32
	f.deps.RunWaitResume = func(_ context.Context, _ string, delivery WaitDelivery) (*promptresult.Result, error) {
		resumes.Add(1)
		return &promptresult.Result{}, delivery.Admitted()
	}
	f.loop.SetDeps(f.deps)
	f.arm(t)
	finish := f.loop.Admission.BeginPromptExecution(t.Context(), "session")
	blocked.Store(true)
	f.loop.Nudges.Nudge(t.Context(), "session", anchor.ProcessFinished, "", "target", anchor.Envelope{})
	f.drain(t, finish)
	if f.prompts.Load() != 0 || resumes.Load() != 0 {
		t.Fatal("failed turn resumed automatically")
	}
	winner, ok := f.loop.Deliveries.waitWinner("session")
	if !ok || winner.retryScheduled.Load() {
		t.Fatalf("wait result missing or retry scheduled: %+v", winner)
	}
	blocked.Store(false)
	f.loop.Nudges.DrainPending(t.Context(), "session")
	f.loop.Turns.WaitForAsyncTurns(testutil.BoundedContext(t, 2*time.Second))
	if _, ok := f.loop.Deliveries.waitWinner("session"); ok || resumes.Load() != 1 {
		t.Fatalf("wait delivery: pending=%v resumes=%d", ok, resumes.Load())
	}
}
