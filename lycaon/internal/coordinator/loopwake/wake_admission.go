package loopwake

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
)

func (pending pendingLoopWake) actionableInput(sessionID string) HostWakeActionableInput {
	return HostWakeActionableInput{
		SessionID: sessionID, Wake: pending.wake, Inform: pending.inform,
		CompletingJobID: pending.completingJobID, Env: pending.env, PostTurnDrain: pending.postTurnDrain,
	}
}

func (l *LoopEngine) matchesActiveWait(ctx context.Context, sessionID string, pending pendingLoopWake) bool {
	if !l.IsSleeping(sessionID) {
		return false
	}
	_, matched := waitConditionForWake(l.activeWaitTriggers(sessionID), waitMatchInput{
		Wake: pending.wake, CompletingJobID: pending.completingJobID, ProcessHandle: pending.legID,
		ProcessHandles: l.ActiveProcessHandles(sessionID), CycleIdle: l.workerCycleIdle(ctx, sessionID, pending.completingJobID),
		OverlayPromoteDue: l.overlayPromoteDue(ctx, sessionID, pending.env), NeedsDecision: pending.env.HasWorkerDecision(),
	})
	return matched
}

// routeWaitWake settles or interrupts the wait before prompt admission.
func (l *LoopEngine) routeWaitWake(ctx context.Context, sessionID string, pending pendingLoopWake) bool {
	wake, inform, legID, completingJobID, env := pending.wake, pending.inform, pending.legID, pending.completingJobID, pending.env
	if l.resumePendingWait(ctx, sessionID, wake) {
		return true
	}
	triggers := l.activeWaitTriggers(sessionID)
	if !l.waitWakeAccepted(ctx, sessionID, wake, inform, legID, completingJobID, env) {
		if shouldDeferForAllWorkersIdle(triggers, wake, completingJobID) && !l.workerCycleIdle(ctx, sessionID, completingJobID) {
			loopLogNudge(sessionID, wake, inform, legID, completingJobID, "defer_subscription_all_idle")
			l.deferNudge(ctx, sessionID, pending)
		} else {
			loopLogNudge(sessionID, wake, inform, legID, completingJobID, "subscription_filtered")
		}
		return true
	}
	if l.IsSleeping(sessionID) || alwaysBreaksSleep(wake, completingJobID) {
		if l.settleDurableWaitWake(ctx, sessionID, wake, inform, legID, completingJobID, env) {
			return true
		}
		l.breakSleep(ctx, sessionID, string(wake), true)
	}
	return false
}

func (l *LoopEngine) resumePendingWait(ctx context.Context, sessionID string, wake anchor.ID) bool {
	if _, ready := l.waitWinner(sessionID); !ready {
		return false
	}
	l.breakSleep(ctx, sessionID, string(wake), false)
	l.runWaitResumeAsync(ctx, sessionID)
	return true
}
