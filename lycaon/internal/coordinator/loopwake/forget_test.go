package loopwake

import (
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/testutil"
	"sync"
	"testing"
	"time"
)

func TestForgetSessionReleasesAllSessionState(t *testing.T) {
	const sessionID = "session-1"
	loop := NewLoopEngine()
	loop.Nudges.pendingQueues.Store(sessionID, &sessionNudgeQueue{})
	loop.Nudges.pendingWorkerQueues.Store(sessionID, &deferredNudgeQueue{})
	loop.Turns.promptActive.Store(sessionID, struct{}{})
	loop.Observations.promptObservedSeq.Store(sessionID, uint64(4))
	loop.Observations.promptWorkflow.Store(sessionID, observedWorkflow{runID: "run-1", revision: 2})
	loop.Nudges.pendingDrain.Store(sessionID, struct{}{})
	loop.Admission.budget.Store(loopBudgetKey{sessionID: sessionID, runID: "run-1"}, 3)
	loop.Nudges.kickDedup.Store(loopKickKey{sessionID: sessionID, runID: "run-1", wake: anchor.LegFinished}, loopKickStamp{at: time.Now()})
	st := loop.Waits.sleep.state(sessionID)
	st.timer = time.AfterFunc(time.Hour, func() {})

	loop.ForgetSession(t.Context(), sessionID)

	for name, state := range map[string]*sync.Map{
		"pending":         &loop.Nudges.pendingQueues,
		"deferred":        &loop.Nudges.pendingWorkerQueues,
		"prompt active":   &loop.Turns.promptActive,
		"prompt sequence": &loop.Observations.promptObservedSeq,
		"prompt workflow": &loop.Observations.promptWorkflow,
		"pending drain":   &loop.Nudges.pendingDrain,
		"sleep":           &loop.Waits.sleep.Map,
	} {
		if _, ok := state.Load(sessionID); ok {
			t.Errorf("%s state survived cleanup", name)
		}
	}
	if st.timer != nil {
		t.Fatal("sleep timer survived cleanup")
	}
	if _, ok := loop.Admission.budget.Load(loopBudgetKey{sessionID: sessionID, runID: "run-1"}); ok {
		t.Fatal("loop budget survived cleanup")
	}
	remaining := 0
	loop.Nudges.kickDedup.Range(func(_, _ any) bool { remaining++; return true })
	if remaining != 0 {
		t.Fatalf("kick dedup entries = %d want 0", remaining)
	}
}

func TestForgetSessionWaitsForFiringTimerBeforeFinalSweep(t *testing.T) {
	const sessionID = "session-1"
	loop := NewLoopEngine()
	timerDone := make(chan struct{})
	st := &sessionSleep{timerDone: timerDone}
	st.mu.Lock()
	loop.Waits.sleep.Store(sessionID, st)
	finished := make(chan struct{})
	go func() {
		loop.ForgetSession(t.Context(), sessionID)
		close(finished)
	}()

	testutil.WaitFor(t, time.Second, func() bool {
		_, ok := loop.Waits.sleep.Load(sessionID)
		return !ok
	})
	loop.Waits.sleep.Store(sessionID, &sessionSleep{})
	st.mu.Unlock()
	select {
	case <-finished:
		t.Fatal("cleanup returned before firing timer completed")
	default:
	}
	close(timerDone)

	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not finish after timer callback completed")
	}
	if _, ok := loop.Waits.sleep.Load(sessionID); ok {
		t.Fatal("timer callback replacement survived final sweep")
	}
}
