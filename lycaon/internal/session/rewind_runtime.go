package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Manager) rollbackWorkerState(ctx context.Context, sess *api.Session, rootID string) {
	if m.workerQueue != nil && m.Workers.Workspaces.Touches != nil {
		jobs, err := m.workerQueue.ListBySession(ctx, sess.ProjectID, rootID)
		if err == nil {
			for _, job := range jobs {
				m.Workers.Workspaces.Touches.ClearJob(job.ID)
			}
		}
	}
	m.Promotion.Forget(rootID)
	m.Runner.Settlement.ReconcileSandbox(ctx, rootID)
}

// rollbackCoordinatorBatch fences kicks from the discarded boundary.
func (m *Manager) rollbackCoordinatorBatch(ctx context.Context, sessionID string) {
	m.Batch.Apply(ctx, sessionID, batch.EventVisibleUserMessage, 0)
	m.Batch.BeginTurn(sessionID)
	rt := m.ensureCoordinatorRuntime()
	rt.Kicks().ClearPending(sessionID)
	rt.CoordinatorLoop().ClearPending(sessionID)
	rt.CoordinatorLoop().InterruptSleep(ctx, sessionID)
}

func (m *Manager) rollbackTurnLedgers(sessionID, rootID string) {
	m.Runner.Closeouts.Rewind(sessionID, rootID)
	for _, key := range []string{sessionID, rootID} {
		m.ProgressClosure.Forget(key)
		m.Runner.History.ForgetCalibration(key)
	}
}

// Progress checklists have no version to restore.
func (m *Manager) rollbackProgress(rootID string) {
	if m == nil || m.progress == nil {
		return
	}
	_ = m.progress.Set(rootID, "")
}

// rollbackQueue reconciles the draft with committed receipt cancellation.
func (m *Manager) rollbackQueue(ctx context.Context, sessionID string) {
	if m.queue == nil {
		return
	}
	m.queue.Clear(sessionID)
	m.Drafts.Publish(ctx, sessionID, m.queue.Snapshot(sessionID).Revision)
	m.Runner.SubmissionState.Publish(context.WithoutCancel(ctx), sessionID)
}
