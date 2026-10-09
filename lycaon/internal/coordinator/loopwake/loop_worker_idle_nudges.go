package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"strings"
)

func (l *Nudges) dropDeferredBudgetRequestForJob(sessionID, jobID string) {
	if l == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(jobID) == "" {
		return
	}
	if l.sessionDeferredQueue(sessionID).removeBudgetRequestForJob(jobID) {
		loopLogNudge(sessionID, anchor.WorkerBudgetRequested, anchor.WorkerBudgetRequested, jobID, "", "drop:job_terminal")
	}
}

func (l *Nudges) deferNudge(ctx context.Context, sessionID string, d pendingLoopWake) {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	if d.seq == 0 {
		d.seq = l.nudgeSeq.Add(1)
	}
	if !d.env.BatchSeqSet {
		live := l.Policy.coordinatorBatchState(ctx, sessionID)
		if live.Seq > 0 {
			d.env.BatchSeq = live.Seq
			d.env.BatchSeqSet = true
		}
	}
	l.sessionDeferredQueue(sessionID).push(d)
}

func (l *Nudges) flushDeferredNudges(ctx context.Context, sessionID string) {
	if l == nil {
		return
	}
	items := l.sessionDeferredQueue(sessionID).drain()
	for _, d := range items {
		l.deliverNudge(ctx, sessionID, d)
	}
}

func (l *Nudges) flushDeferredWhenWorkerCycleIdle(ctx context.Context, sessionID, completingJobID string) {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	if !l.Cycles.workerCycleIdle(ctx, sessionID, completingJobID) {
		return
	}
	l.flushDeferredNudges(ctx, sessionID)
}
