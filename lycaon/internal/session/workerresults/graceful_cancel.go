package workerresults

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

type GracefulCancel struct {
	mu      sync.Mutex
	pending map[string]*gracefulCancelRegistration
}

func NewGracefulCancel() *GracefulCancel { return &GracefulCancel{} }

type gracefulCancelRegistration struct {
	jobID  string
	reason string
}

// Register schedules closeout for the worker's next iteration.
// The registration survives request cancellation.
func (m *GracefulCancel) Register(childSessionID, jobID, reason string) error {
	if m == nil {
		return fmt.Errorf("worker closeout service not configured")
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

// Pending reports whether the worker child should stop its tool loop.
func (m *GracefulCancel) Pending(_ context.Context, sess *api.Session) (string, bool) {
	if m == nil || sess == nil || !sess.IsWorkerChild() {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	wait, ok := m.pending[sess.ID]
	if !ok || wait == nil {
		return "", false
	}
	return wait.reason, true
}

// Closeout reads the registration retained until closeout finishes.
func (m *GracefulCancel) Closeout(childSessionID string) (jobID, reason string, ok bool) {
	if m == nil {
		return "", "", false
	}
	childSessionID = strings.TrimSpace(childSessionID)
	m.mu.Lock()
	defer m.mu.Unlock()
	wait, ok := m.pending[childSessionID]
	if !ok || wait == nil {
		return "", "", false
	}
	return wait.jobID, wait.reason, true
}

// Finish consumes the registration once closeout has run. There is
// no request context here, so the registration outlives the request that started the
// cancel; the executor reports the closeout outcome through its own return value.
func (m *GracefulCancel) Finish(childSessionID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	delete(m.pending, strings.TrimSpace(childSessionID))
	m.mu.Unlock()
}

func (m *GracefulCancel) storeWorkerGracefulCancel(childSessionID string, wait *gracefulCancelRegistration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil {
		m.pending = make(map[string]*gracefulCancelRegistration)
	}
	m.pending[childSessionID] = wait
}
