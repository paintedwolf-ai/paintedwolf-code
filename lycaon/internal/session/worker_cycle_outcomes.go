package session

import (
	"context"
	"time"
)

// NotifyWorkerCycleTerminal flushes deferred loop wakes when the parent worker cycle is idle.
func (m *Manager) NotifyWorkerCycleTerminal(ctx context.Context, parentID, completingJobID string) {
	if m == nil {
		return
	}
	m.ensureCoordinatorRuntime().CoordinatorLoop().OnWorkerCycleTerminal(ctx, parentID, completingJobID)
	m.Batch.Reconcile(ctx, parentID)
	m.Batch.DisarmTerminal(ctx, parentID)
	m.Runner.Settlement.ReconcileSandbox(ctx, parentID)
	m.Admission.MaybePromote(ctx, parentID)
	m.Workers.Digests.Forget(completingJobID)
}

// NudgeLegFinishedLoopWake queues leg-finished loop wake for tests and delegation outcomes.
func (m *Manager) NudgeLegFinishedLoopWake(ctx context.Context, parentID string, completedAt time.Time, legID string) {
	if m == nil {
		return
	}
	m.nudgeLegFinished(ctx, parentID, completedAt, legID)
}
