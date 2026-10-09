package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"strings"
)

func (l *Nudges) sessionPendingQueue(sessionID string) *sessionNudgeQueue {
	if v, ok := l.pendingQueues.Load(sessionID); ok {
		return v.(*sessionNudgeQueue)
	}
	q := &sessionNudgeQueue{}
	actual, _ := l.pendingQueues.LoadOrStore(sessionID, q)
	return actual.(*sessionNudgeQueue)
}

func (l *Nudges) sessionDeferredQueue(sessionID string) *deferredNudgeQueue {
	if v, ok := l.pendingWorkerQueues.Load(sessionID); ok {
		return v.(*deferredNudgeQueue)
	}
	q := &deferredNudgeQueue{}
	actual, _ := l.pendingWorkerQueues.LoadOrStore(sessionID, q)
	return actual.(*deferredNudgeQueue)
}

func (l *Nudges) enqueuePending(sessionID string, pending pendingLoopWake) {
	l.sessionPendingQueue(sessionID).push(pending)
}

func (l *Nudges) deferPromptWake(ctx context.Context, sessionID string, pending pendingLoopWake) {
	pending.postTurnDrain = true
	l.enqueuePending(sessionID, pending)
	// The busy owner may have released before this enqueue.
	l.schedulePendingDrain(context.WithoutCancel(ctx), sessionID, true)
}

func (l *Nudges) schedulePendingDrain(ctx context.Context, sessionID string, postTurn bool) {
	if _, draining := l.pendingDrain.Load(sessionID); draining {
		return
	}
	if l.Admission.PromptExecutionActive(sessionID) {
		return
	}
	if _, active := l.Turns.promptActive.Load(sessionID); active {
		return
	}
	if l.Turns.hostTurnBlocked(ctx, sessionID) {
		l.notifyLoopQuiescent(ctx, sessionID)
		return
	}
	if _, ready := l.Deliveries.waitWinner(sessionID); ready {
		l.Waits.breakSleep(ctx, sessionID, "wait resolved", false)
		l.Deliveries.runWaitResumeAsync(ctx, sessionID)
		return
	}
	if _, pending := l.sessionPendingQueue(sessionID).peek(); !pending {
		l.notifyLoopQuiescent(ctx, sessionID)
		return
	}
	l.Turns.spawnAsyncTurn(ctx, sessionID, func(ctx context.Context) {
		l.drainPending(ctx, sessionID, postTurn)
	})
}

func (l *Nudges) HasPendingLoopWakes(sessionID string) bool {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	if _, draining := l.pendingDrain.Load(sessionID); draining {
		return true
	}
	if _, ok := l.Deliveries.waitWinner(sessionID); ok {
		return true
	}
	if _, ok := l.Pending(sessionID); ok {
		return true
	}
	return l.sessionDeferredQueue(sessionID).nonEmpty()
}

func (l *Nudges) drainPending(ctx context.Context, sessionID string, postTurnDrain bool) {
	if _, draining := l.pendingDrain.LoadOrStore(sessionID, struct{}{}); draining {
		return
	}
	defer func() {
		l.pendingDrain.Delete(sessionID)
		l.schedulePendingDrain(ctx, sessionID, postTurnDrain)
	}()
	for {
		if l.Turns.hostTurnBlocked(ctx, sessionID) {
			return
		}
		// Worker-cycle deferrals are reconsidered only after outcome acknowledgement.
		if l.Cycles.workerCycleIdle(ctx, sessionID, "") {
			l.flushDeferredNudges(ctx, sessionID)
		}
		q := l.sessionPendingQueue(sessionID)
		pending, ok := q.pop()
		if !ok {
			return
		}
		pending.postTurnDrain = pending.postTurnDrain || postTurnDrain
		if !l.Turns.runPromptSync(ctx, sessionID, pending) {
			return
		}
	}
}

func (l *Nudges) kickStillQueued(sessionID string, wake anchor.ID) bool {
	deps := l.loopDeps()
	if deps.HasQueuedKick == nil {
		return false
	}
	kickID := anchor.InformRender(wake)
	return kickID != "" && deps.HasQueuedKick(sessionID, kickID)
}
