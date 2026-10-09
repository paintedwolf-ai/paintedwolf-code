package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
)

func (l *WaitSubscriptions) matchesActiveWait(ctx context.Context, sessionID string, pending pendingLoopWake) bool {
	if !l.Waits.IsSleeping(sessionID) {
		return false
	}
	_, matched := waitConditionForWake(l.Waits.Triggers(sessionID), waitMatchInput{
		Wake: pending.wake, CompletingJobID: pending.completingJobID, ProcessHandle: pending.legID,
		ProcessHandles: l.Waits.ActiveProcessHandles(sessionID), WorkerHandles: l.Waits.activeWorkerHandles(sessionID),
		CycleIdle:         l.Cycles.workerCycleIdle(ctx, sessionID, pending.completingJobID),
		OverlayPromoteDue: l.overlayPromoteDue(ctx, sessionID, pending.env), NeedsDecision: pending.env.HasWorkerDecision(),
	})
	return matched
}

func (l *WaitSubscriptions) routeWaitWake(ctx context.Context, sessionID string, pending pendingLoopWake) bool {
	wake, inform, legID, completingJobID, env := pending.wake, pending.inform, pending.legID, pending.completingJobID, pending.env
	if l.resumePendingWait(ctx, sessionID, wake) {
		return true
	}
	triggers := l.Waits.Triggers(sessionID)
	if !l.waitWakeAccepted(ctx, sessionID, wake, inform, legID, completingJobID, env) {
		if shouldDeferForAllWorkersIdle(triggers, wake, completingJobID) && !l.Cycles.workerCycleIdle(ctx, sessionID, completingJobID) {
			loopLogNudge(sessionID, wake, inform, legID, completingJobID, "defer_subscription_all_idle")
			l.Nudges.deferNudge(ctx, sessionID, pending)
		} else {
			loopLogNudge(sessionID, wake, inform, legID, completingJobID, "subscription_filtered")
		}
		return true
	}
	if l.Waits.IsSleeping(sessionID) || alwaysBreaksSleep(wake, completingJobID) {
		if l.settleDurableWaitWake(ctx, sessionID, wake, inform, legID, completingJobID, env) {
			return true
		}
		l.Waits.breakSleep(ctx, sessionID, string(wake), true)
	}
	return false
}

func (l *WaitSubscriptions) resumePendingWait(ctx context.Context, sessionID string, wake anchor.ID) bool {
	if _, ready := l.Deliveries.waitWinner(sessionID); !ready {
		return false
	}
	l.Waits.breakSleep(ctx, sessionID, string(wake), false)
	l.Deliveries.runWaitResumeAsync(ctx, sessionID)
	return true
}
