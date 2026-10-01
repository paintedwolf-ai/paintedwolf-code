package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/pkg/api"
)

// rollbackEphemeralState reconciles volatile state with the truncated timeline.
func (m *Manager) rollbackEphemeralState(ctx context.Context, sess *api.Session) {
	if m == nil || sess == nil {
		return
	}
	sessionID := strings.TrimSpace(sess.ID)
	rootID := RootSessionID(ctx, m.store, sessionID)

	m.checkpointCapture.Reset(rootID)
	m.rollbackWorkerState(ctx, sess, rootID)
	m.rollbackCoordinatorBatch(ctx, sessionID)
	m.rollbackTurnLedgers(sessionID, rootID)
	m.rollbackProgress(rootID)
	m.rollbackQueue(ctx, sessionID)
}

func (m *Manager) rollbackWorkerState(ctx context.Context, sess *api.Session, rootID string) {
	if m.workerQueue != nil && m.workerTouches != nil {
		jobs, err := m.workerQueue.ListBySession(ctx, sess.ProjectID, rootID)
		if err == nil {
			for _, job := range jobs {
				m.workerTouches.ClearJob(job.ID)
			}
		}
	}
	m.mergeReconcile.Delete(rootID)
	m.promotePathStatus.Delete(rootID)
	m.maybeReconcileSandboxesOnIdle(ctx, rootID)
}

// rollbackCoordinatorBatch fences kicks from the discarded boundary.
func (m *Manager) rollbackCoordinatorBatch(ctx context.Context, sessionID string) {
	m.applyCoordinatorBatchEvent(ctx, sessionID, batch.EventVisibleUserMessage, 0)
	m.resetCoordinatorBatchTurnGuard(sessionID)
	rt := m.ensureCoordinatorRuntime()
	rt.Kicks().ClearPending(sessionID)
	rt.CoordinatorLoop().ClearPending(sessionID)
	rt.CoordinatorLoop().InterruptSleep(ctx, sessionID)
}

func (m *Manager) rollbackTurnLedgers(sessionID, rootID string) {
	m.closeout.rewind(sessionID, rootID)
	for _, key := range []string{sessionID, rootID} {
		m.progressClosureExpect.Delete(key)
		m.compactionTokenCalibration.Delete(key)
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
	m.publishQueue(ctx, sessionID, m.queue.Snapshot(sessionID).Revision)
	m.publishSessionState(context.WithoutCancel(ctx), sessionID)
}
