package loopwake

import (
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestForgetSessionReleasesAllSessionState(t *testing.T) {
	const sessionID = "session-1"
	loop := NewLoopEngine()
	loop.pendingQueues.Store(sessionID, &sessionNudgeQueue{})
	loop.pendingWorkerQueues.Store(sessionID, &deferredNudgeQueue{})
	loop.promptActive.Store(sessionID, struct{}{})
	loop.promptObservedSeq.Store(sessionID, uint64(4))
	loop.promptWorkflow.Store(sessionID, observedWorkflow{runID: "run-1", revision: 2})
	loop.pendingDrain.Store(sessionID, struct{}{})
	loop.budget.Store(loopBudgetKey{sessionID: sessionID, runID: "run-1"}, 3)
	loop.kickDedup.Store(loopKickKey{sessionID: sessionID, runID: "run-1", wake: anchor.LegFinished}, loopKickStamp{at: time.Now()})
	st := loop.sleep.state(sessionID)
	st.timer = time.AfterFunc(time.Hour, func() {})

	loop.ForgetSession(t.Context(), sessionID)

	for name, state := range map[string]*sync.Map{
		"pending":         &loop.pendingQueues,
		"deferred":        &loop.pendingWorkerQueues,
		"prompt active":   &loop.promptActive,
		"prompt sequence": &loop.promptObservedSeq,
		"prompt workflow": &loop.promptWorkflow,
		"pending drain":   &loop.pendingDrain,
		"sleep":           &loop.sleep.Map,
	} {
		if _, ok := state.Load(sessionID); ok {
			t.Errorf("%s state survived cleanup", name)
		}
	}
	if st.timer != nil {
		t.Fatal("sleep timer survived cleanup")
	}
	if _, ok := loop.budget.Load(loopBudgetKey{sessionID: sessionID, runID: "run-1"}); ok {
		t.Fatal("loop budget survived cleanup")
	}
	remaining := 0
	loop.kickDedup.Range(func(_, _ any) bool { remaining++; return true })
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
	loop.sleep.Store(sessionID, st)
	finished := make(chan struct{})
	go func() {
		loop.ForgetSession(t.Context(), sessionID)
		close(finished)
	}()

	testutil.WaitFor(t, time.Second, func() bool {
		_, ok := loop.sleep.Load(sessionID)
		return !ok
	})
	loop.sleep.Store(sessionID, &sessionSleep{})
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
	if _, ok := loop.sleep.Load(sessionID); ok {
		t.Fatal("timer callback replacement survived final sweep")
	}
}
