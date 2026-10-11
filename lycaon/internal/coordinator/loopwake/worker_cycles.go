package loopwake

import (
	"context"
	"log/slog"
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

type WorkerCyclesDeps struct {
	GetSession      func(ctx context.Context, sessionID string) (*api.Session, error)
	WorkerCycleIdle WorkerCycleIdle
}
type WorkerCycles struct {
	depsMu        sync.RWMutex
	deps          WorkerCyclesDeps
	Nudges        *Nudges
	Subscriptions *WaitSubscriptions
	Waits         *Waits
}

func (l *WorkerCycles) setDeps(deps WorkerCyclesDeps) {
	l.depsMu.Lock()
	l.deps = deps
	l.depsMu.Unlock()
}
func (l *WorkerCycles) loopDeps() WorkerCyclesDeps {
	if l == nil {
		return WorkerCyclesDeps{}
	}
	l.depsMu.RLock()
	defer l.depsMu.RUnlock()
	return l.deps
}
func (l *WorkerCycles) WorkerCycleIsIdle(ctx context.Context, sessionID string) bool {
	return l.workerCycleIdle(ctx, sessionID, "")
}
func (l *WorkerCycles) workerCycleIdle(ctx context.Context, sessionID, completingJobID string) bool {
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
func (l *WorkerCycles) OnWorkerCycleTerminal(ctx context.Context, sessionID, completingJobID string) {
	l.Nudges.dropDeferredBudgetRequestForJob(sessionID, completingJobID)
	l.Subscriptions.resolveSubscribedWorkerWait(ctx, sessionID, completingJobID)
	l.Nudges.flushDeferredWhenWorkerCycleIdle(ctx, sessionID, completingJobID)
	if !l.workerCycleIdle(ctx, sessionID, completingJobID) {
		return
	}
	l.Waits.maybeDisarmTimerOnBatchTerminal(ctx, sessionID)
	// Outcome acknowledgement releases the worker slot before the coordinator turn.
	l.Nudges.schedulePendingDrain(ctx, sessionID, false)
}
