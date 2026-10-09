package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"time"
)

func (l *Waits) DisarmTimerBackstop(ctx context.Context, sessionID string) {
	if l == nil {
		return
	}
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	cancelSleepTimerLocked(st)
	if len(st.waitTriggers) == 0 {
		st.mu.Unlock()
		return
	}
	var kept []WaitTrigger
	for _, t := range st.waitTriggers {
		if t == WaitTriggerTimer {
			continue
		}
		kept = append(kept, t)
	}
	st.waitTriggers = kept
	var closed WaitLease
	var hadLease bool
	if len(kept) == 0 {
		st.armed = false
		st.until = time.Time{}
		st.reason = ""
		closed, hadLease = closeWaitLeaseLocked(st, sessionID)
	}
	st.mu.Unlock()
	if hadLease {
		l.publishWaitLease(ctx, closed)
	}
}

func (l *Waits) maybeDisarmTimerOnBatchTerminal(ctx context.Context, sessionID string) {
	live := l.Policy.coordinatorBatchState(ctx, sessionID)
	switch live.Phase {
	case batch.PhaseSynthesize, batch.PhaseClosed:
		l.DisarmTimerBackstop(ctx, sessionID)
	}
}

func (l *Waits) disarmTimerOnClosedBatch(ctx context.Context, sessionID string, live batch.State) {
	l.DisarmTimerBackstop(ctx, sessionID)
	l.Policy.dropClosedBatchPendingKicks(ctx, sessionID, live)
}
