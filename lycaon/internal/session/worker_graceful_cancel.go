package session

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

type gracefulCancelRegistration struct {
	jobID  string
	reason string
}

// RegisterWorkerGracefulCancel schedules closeout for the worker's next iteration.
// The registration survives request cancellation.
func (m *Manager) RegisterWorkerGracefulCancel(childSessionID, jobID, reason string) error {
	if m == nil {
		return fmt.Errorf("session manager not configured")
	}
	childSessionID = strings.TrimSpace(childSessionID)
	jobID = strings.TrimSpace(jobID)
	if childSessionID == "" || jobID == "" {
		return fmt.Errorf("child session and job id required")
	}
	m.storeWorkerGracefulCancel(childSessionID, &gracefulCancelRegistration{
		jobID:  jobID,
		reason: strings.TrimSpace(reason),
	})
	return nil
}

// WorkerGracefulCancelPending reports whether the worker child should stop its tool loop.
func (m *Manager) WorkerGracefulCancelPending(_ context.Context, sess *api.Session) (string, bool) {
	if m == nil || sess == nil || !sess.IsWorkerChild() {
		return "", false
	}
	m.workerGracefulCancelMu.Lock()
	defer m.workerGracefulCancelMu.Unlock()
	wait, ok := m.workerGracefulCancel[sess.ID]
	if !ok || wait == nil {
		return "", false
	}
	return wait.reason, true
}

// TakeWorkerGracefulCancel consumes a pending graceful cancel for executor closeout.
func (m *Manager) TakeWorkerGracefulCancel(childSessionID string) (jobID, reason string, ok bool) {
	if m == nil {
		return "", "", false
	}
	childSessionID = strings.TrimSpace(childSessionID)
	m.workerGracefulCancelMu.Lock()
	defer m.workerGracefulCancelMu.Unlock()
	wait, ok := m.workerGracefulCancel[childSessionID]
	if !ok || wait == nil {
		return "", "", false
	}
	return wait.jobID, wait.reason, true
}

// FinishWorkerGracefulCancel consumes the registration once closeout has run. There is
// no request context here, so the registration outlives the request that started the
// cancel; the executor reports the closeout outcome through its own return value.
func (m *Manager) FinishWorkerGracefulCancel(childSessionID string) {
	if m == nil {
		return
	}
	m.workerGracefulCancelMu.Lock()
	delete(m.workerGracefulCancel, strings.TrimSpace(childSessionID))
	m.workerGracefulCancelMu.Unlock()
}

func (m *Manager) storeWorkerGracefulCancel(childSessionID string, wait *gracefulCancelRegistration) {
	m.workerGracefulCancelMu.Lock()
	defer m.workerGracefulCancelMu.Unlock()
	if m.workerGracefulCancel == nil {
		m.workerGracefulCancel = make(map[string]*gracefulCancelRegistration)
	}
	m.workerGracefulCancel[childSessionID] = wait
}
