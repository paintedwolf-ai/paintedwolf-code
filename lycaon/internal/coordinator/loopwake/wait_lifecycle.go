package loopwake

import (
	"context"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"strings"
	"sync"
	"time"
)

type WaitsDeps struct {
	PublishWaitLease func(ctx context.Context, sessionID string, lease WaitLease)
}
type Waits struct {
	depsMu        sync.RWMutex
	deps          WaitsDeps
	sleep         sessionSleeps
	Policy        *HostWakePolicy
	Nudges        *Nudges
	Facts         *SessionFacts
	Deliveries    *WaitDeliveries
	Subscriptions *WaitSubscriptions
	Cycles        *WorkerCycles
}

func (l *Waits) setDeps(deps WaitsDeps) { l.depsMu.Lock(); l.deps = deps; l.depsMu.Unlock() }
func (l *Waits) loopDeps() WaitsDeps {
	if l == nil {
		return WaitsDeps{}
	}
	l.depsMu.RLock()
	defer l.depsMu.RUnlock()
	return l.deps
}
func (l *Waits) InterruptSleep(ctx context.Context, sessionID string) {
	if store := l.Subscriptions.durableWaitStore(); store != nil {
		_ = store.InterruptSession(ctx, sessionID, "interrupted")
	}
	l.Deliveries.waitWinners.Delete(strings.TrimSpace(sessionID))
	l.breakSleep(ctx, sessionID, "user_prompt", false)
	l.Nudges.ClearPending(sessionID)
}
func (l *Waits) EnterSleep(
	ctx context.Context,
	sessionID string,
	until time.Time,
	reason string,
	triggers []WaitTrigger,
	processHandles []string,
	mover SleepMover,
) {
	l.enterSleep(ctx, sessionID, sleepArm{
		until: until, reason: reason, triggers: triggers, processHandles: processHandles, mover: mover,
	})
}
func (l *Waits) enterSleep(ctx context.Context, sessionID string, arm sleepArm) {
	if l == nil || strings.TrimSpace(sessionID) == "" {
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
		loopLogSleep(sessionID, "timer_fire", reason, until)
		l.Nudges.Nudge(wakeCtx, sessionID, anchor.WaitTimerFired, anchor.WaitTimerFired, "", anchor.Envelope{})
	})
}
func (l *Waits) MarkWaitCalled(sessionID string) {
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	st.waitThisTurn = true
	st.mu.Unlock()
}
func (l *Waits) parkForActiveHold(ctx context.Context, sessionID string) bool {
	if l == nil {
		return false
	}
	if l.Facts.sessionHasPendingUserInput(ctx, sessionID) {
		l.ParkForPendingUserInput(ctx, sessionID, "pending user ask")
		return true
	}
	maxSleep := time.Now().UTC().Add(l.Facts.sessionLimits(ctx, sessionID).CoordinatorMaxSleep())
	overlayPromoteDue := l.Subscriptions.overlayPromoteDue(ctx, sessionID, anchor.Envelope{})
	if l.Facts.sessionHumanApprovalAwaiting(ctx, sessionID) {
		l.EnterSleep(
			ctx,
			sessionID,
			maxSleep,
			"awaiting human approval",
			AwaitUserWaitTriggers(overlayPromoteDue),
			nil,
			SleepMoverUser,
		)
		return true
	}
	// The host observer owns this phase's wake.
	if l.Facts.sessionHostObligationHeld(ctx, sessionID) {
		l.EnterSleep(
			ctx,
			sessionID,
			maxSleep,
			l.Facts.hostObligationParkReason(ctx, sessionID),
			HostObligationWaitTriggers(overlayPromoteDue),
			nil,
			SleepMoverHost,
		)
		return true
	}
	return false
}
func (l *Waits) OnTurnComplete(ctx context.Context, sessionID string, hostTurn bool) UserTurnContinuation {
	if l == nil {
		return UserTurnSettled
	}
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	waited := st.waitThisTurn
	st.waitThisTurn = false
	st.mu.Unlock()
	if waited {
		loopLogSleep(sessionID, "turn_complete_wait", "wait() called", time.Time{})
		return UserTurnContinues
	}
	if l.parkForActiveHold(ctx, sessionID) {
		return UserTurnContinues
	}
	cycleIdle := l.Cycles.WorkerCycleIsIdle(ctx, sessionID)
	// Unmet phase obligations re-arm a workflow wake instead of the awaiting-user park.
	if cycleIdle && l.Facts.workflowObligationsOpen(ctx, sessionID) {
		overlayPromoteDue := l.Subscriptions.overlayPromoteDue(ctx, sessionID, anchor.Envelope{})
		l.EnterSleep(
			ctx,
			sessionID,
			time.Now().UTC().Add(defaultWorkflowObligationInterval),
			"workflow obligations open",
			WorkflowObligationWaitTriggers(overlayPromoteDue),
			nil,
			SleepMoverHost,
		)
		return UserTurnContinues
	}
	if hostTurn && cycleIdle {
		overlayPromoteDue := l.Subscriptions.overlayPromoteDue(ctx, sessionID, anchor.Envelope{})
		l.EnterSleep(
			ctx,
			sessionID,
			time.Now().UTC().Add(l.Facts.sessionLimits(ctx, sessionID).CoordinatorMaxSleep()),
			"awaiting user after idle host turn",
			AwaitUserWaitTriggers(overlayPromoteDue),
			nil,
			SleepMoverUser,
		)
		return UserTurnSettled
	}
	if hostTurn {
		loopLogSleep(sessionID, "turn_complete_no_park", "host turn without wait()", time.Time{})
	}
	if !cycleIdle {
		return UserTurnContinues
	}
	return UserTurnSettled
}
func (l *Waits) breakSleep(ctx context.Context, sessionID, reason string, preserveDeadline bool) {
	if l == nil {
		return
	}
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	cancelSleepTimerLocked(st)
	if !st.until.IsZero() {
		loopLogSleep(sessionID, "break", reason, st.until)
	}
	if !preserveDeadline {
		st.interruptedUntil = time.Time{}
	} else if !st.until.IsZero() && st.until.After(time.Now().UTC()) {
		st.interruptedUntil = st.until.UTC()
	}
	st.armed = false
	st.until = time.Time{}
	st.reason = ""
	closed, hadLease := closeWaitLeaseLocked(st, sessionID)
	st.mu.Unlock()
	if hadLease {
		l.publishWaitLease(ctx, closed)
	}
}
func (l *Waits) ResolveWaitUntil(ctx context.Context, sessionID string, resume bool, requested time.Duration) (until time.Time, resumed bool) {
	now := time.Now().UTC()
	if !resume {
		return now.Add(l.Facts.capSleepDuration(ctx, sessionID, requested)), false
	}
	if requested <= 0 {
		requested = defaultCoordinatorWaitInterval
	}
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return now.Add(l.Facts.capSleepDuration(ctx, sessionID, requested)), false
	}
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	interrupted := st.interruptedUntil
	st.interruptedUntil = time.Time{}
	st.mu.Unlock()
	if !interrupted.IsZero() && interrupted.After(now) {
		return interrupted, true
	}
	return now.Add(l.Facts.capSleepDuration(ctx, sessionID, requested)), false
}
func (l *Waits) IsSleeping(sessionID string) bool {
	if l == nil {
		return false
	}
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return sleepArmedLocked(st, time.Now())
}
