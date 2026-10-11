package loopwake

import (
	"context"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"strings"
	"time"
)

func (l *Waits) enterSleep(ctx context.Context, sessionID string, arm sleepArm) {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	ctx, finish, err := l.timerWork.Begin(ctx)
	if err != nil {
		return
	}
	defer finish()
	l.sleep.mu.Lock()
	if ctx.Err() != nil {
		l.sleep.mu.Unlock()
		return
	}
	triggers := arm.triggers
	if triggers == nil {
		triggers = DefaultCoordinatorWaitTriggers(l.Subscriptions.overlayPromoteDue(ctx, sessionID, anchor.Envelope{}))
	}
	st := l.sleep.state(sessionID)
	// Preserve context values for the later timer wake.
	wakeCtx := context.WithoutCancel(ctx)

	st.mu.Lock()
	// A re-arm retires the previous lease before opening another.
	closed, hadLease := closeWaitLeaseLocked(st, sessionID)
	cancelSleepTimerLocked(st)
	st.armed = true
	st.untilComplete = arm.untilComplete
	st.until = arm.until.UTC()
	st.interruptedUntil = time.Time{}
	st.reason = strings.TrimSpace(arm.reason)
	st.waitTriggers = dedupeWaitTriggers(triggers)
	st.processHandles = normalizeProcessHandles(arm.processHandles)
	st.workerHandles = normalizeProcessHandles(arm.workerHandles)
	st.mover = arm.mover
	opened := openWaitLeaseLocked(st, sessionID, arm.mover)
	loopLogSleep(sessionID, "arm", arm.reason, st.until)
	l.armSleepTimerLocked(st, wakeCtx, sessionID, arm.reason)
	st.mu.Unlock()
	l.sleep.mu.Unlock()

	if hadLease {
		l.publishWaitLease(ctx, closed)
	}
	if opened.ActivityID != "" {
		l.publishWaitLease(ctx, opened)
	}
}
func (l *Waits) armSleepTimerLocked(st *sessionSleep, wakeCtx context.Context, sessionID, reason string) {
	if _, ok := waitTriggerSet(st.waitTriggers)[WaitTriggerTimer]; !ok {
		st.timer = nil
		return
	}
	remaining := time.Until(st.until)
	if remaining <= 0 {
		remaining = time.Millisecond
	}
	generation := st.timerGeneration
	timerDone := make(chan struct{})
	st.timerDone = timerDone
	st.timer = time.AfterFunc(remaining, func() {
		defer close(timerDone)
		callbackCtx, finish, err := l.timerWork.Begin(wakeCtx)
		if err != nil {
			return
		}
		defer finish()
		wakeCtx = callbackCtx
		if wakeCtx.Err() != nil {
			return
		}
		st.mu.Lock()
		if st.timerGeneration != generation {
			st.mu.Unlock()
			return
		}
		until := st.until
		st.timer = nil
		st.timerGeneration++
		// The deadline also retires the activity lease.
		expired, hadLease := closeWaitLeaseLocked(st, sessionID)
		st.mu.Unlock()
		if store := l.Subscriptions.durableWaitStore(); store != nil {
			winner := awaitstore.Condition{Kind: "timer", Outcome: "timed_out"}
			if lease, active, _ := store.ForSession(wakeCtx, sessionID); active {
				if won, _ := store.SettleLease(wakeCtx, lease.ID, "timed_out", winner); won {
					l.Deliveries.rememberWaitWinner(sessionID, lease.ID, winner)
				}
			}
		}
		if hadLease {
			l.publishWaitLease(wakeCtx, expired)
		}
		if wakeCtx.Err() != nil {
			return
		}
		loopLogSleep(sessionID, "timer_fire", reason, until)
		l.Nudges.Nudge(wakeCtx, sessionID, anchor.WaitTimerFired, anchor.WaitTimerFired, "", anchor.Envelope{})
	})
}
