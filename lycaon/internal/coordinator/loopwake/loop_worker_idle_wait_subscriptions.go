package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"strings"
)

func (l *WaitSubscriptions) resolveSubscribedWorkerWait(ctx context.Context, sessionID, jobID string) {
	store := l.durableWaitStore()
	if store == nil || strings.TrimSpace(jobID) == "" {
		return
	}
	lease, active, err := store.ForSession(ctx, sessionID)
	if err != nil || !active || lease.WorkerJobID != "" {
		return
	}
	triggers, _ := triggersFromConditions(lease.Conditions)
	winner, matched := waitConditionForWake(triggers, waitMatchInput{
		Wake: anchor.WorkerTaskFinished, CompletingJobID: jobID,
		CycleIdle: l.Cycles.workerCycleIdle(ctx, sessionID, jobID),
	})
	if !matched {
		return
	}
	winner.Outcome = "satisfied"
	won, err := store.SettleLease(ctx, lease.ID, "resolved", winner)
	if err != nil || !won {
		return
	}
	l.Deliveries.rememberWaitWinner(sessionID, lease.ID, winner)
	l.Waits.breakSleep(ctx, sessionID, string(anchor.WorkerTaskFinished), false)
	l.Deliveries.runWaitResumeAsync(ctx, sessionID)
}
