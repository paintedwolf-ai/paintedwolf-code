package loopwake

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerCycleIdle excludes the job whose terminal summary is landing.
type WorkerCycleIdle func(ctx context.Context, sess *api.Session, completingJobID string) (bool, error)

// WorkerCycleIsIdle reports whether no task() jobs are pending or running on the parent session.
func (l *LoopEngine) WorkerCycleIsIdle(ctx context.Context, sessionID string) bool {
	return l.workerCycleIdle(ctx, sessionID, "")
}

// workerCycleIdle blocks only on a confirmed active worker cycle.
func (l *LoopEngine) workerCycleIdle(ctx context.Context, sessionID, completingJobID string) bool {
	if l == nil {
		return true
	}
	deps := l.loopDeps()
	if deps.WorkerCycleIdle == nil {
		return true
	}
	sess, err := deps.GetSession(ctx, sessionID)
	if err != nil || sess == nil {
		return true
	}
	idle, err := deps.WorkerCycleIdle(ctx, sess, completingJobID)
	if err != nil {
		slog.WarnContext(ctx, "worker cycle read failed; treating the cycle as idle so the wake is not stranded",
			"component", "coordinator_loop", "session_id", sessionID, "error", err)
		return true
	}
	return idle
}

// dropDeferredBudgetRequestForJob removes a terminal job's budget-request wake;
// the finish wake reports any request still open on the job.
func (l *LoopEngine) dropDeferredBudgetRequestForJob(sessionID, jobID string) {
	if l == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(jobID) == "" {
		return
	}
	if l.sessionDeferredQueue(sessionID).removeBudgetRequestForJob(jobID) {
		loopLogNudge(sessionID, anchor.WorkerBudgetRequested, anchor.WorkerBudgetRequested, jobID, "", "drop:job_terminal")
	}
}

func (l *LoopEngine) deferNudge(ctx context.Context, sessionID string, d pendingLoopWake) {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	if d.seq == 0 {
		d.seq = l.nudgeSeq.Add(1)
	}
	if !d.env.BatchSeqSet {
		live := l.coordinatorBatchState(ctx, sessionID)
		if live.Seq > 0 {
			d.env.BatchSeq = live.Seq
			d.env.BatchSeqSet = true
		}
	}
	l.sessionDeferredQueue(sessionID).push(d)
}

func (l *LoopEngine) flushDeferredNudges(ctx context.Context, sessionID string) {
	if l == nil {
		return
	}
	items := l.sessionDeferredQueue(sessionID).drain()
	for _, d := range items {
		l.deliverNudge(ctx, sessionID, d)
	}
}

func (l *LoopEngine) flushDeferredWhenWorkerCycleIdle(ctx context.Context, sessionID, completingJobID string) {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	if !l.workerCycleIdle(ctx, sessionID, completingJobID) {
		return
	}
	l.flushDeferredNudges(ctx, sessionID)
}

// OnWorkerCycleTerminal drains wakes after a worker summary lands.
func (l *LoopEngine) OnWorkerCycleTerminal(ctx context.Context, sessionID, completingJobID string) {
	l.dropDeferredBudgetRequestForJob(sessionID, completingJobID)
	l.resolveSubscribedWorkerWait(ctx, sessionID, completingJobID)
	l.flushDeferredWhenWorkerCycleIdle(ctx, sessionID, completingJobID)
	if !l.workerCycleIdle(ctx, sessionID, completingJobID) {
		return
	}
	l.maybeDisarmTimerOnBatchTerminal(ctx, sessionID)
	// Outcome acknowledgement releases the worker slot before the coordinator turn.
	l.schedulePendingDrain(ctx, sessionID, false)
}

// Terminal acknowledgement satisfies subscribed waits, including on cancellation.
func (l *LoopEngine) resolveSubscribedWorkerWait(ctx context.Context, sessionID, jobID string) {
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
		CycleIdle: l.workerCycleIdle(ctx, sessionID, jobID),
	})
	if !matched {
		return
	}
	winner.Outcome = "satisfied"
	won, err := store.SettleLease(ctx, lease.ID, "resolved", winner)
	if err != nil || !won {
		return
	}
	l.rememberWaitWinner(sessionID, lease.ID, winner)
	l.breakSleep(ctx, sessionID, string(anchor.WorkerTaskFinished), false)
	l.runWaitResumeAsync(ctx, sessionID)
}
