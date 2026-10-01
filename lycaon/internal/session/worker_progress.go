package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/workerprogress"
	"github.com/lycaon/lycaon/pkg/api"
)

type workerProgressPublisher interface {
	PublishWorkerProgress(ctx context.Context, workerJobID string, snap workerprogress.Snapshot, checkpoint bool) error
}

func (m *Manager) publishWorkerProgress(ctx context.Context, workerJobID string, snap workerprogress.Snapshot, checkpoint bool) {
	if m == nil || strings.TrimSpace(workerJobID) == "" {
		return
	}
	pub, ok := m.workerQueue.(workerProgressPublisher)
	if !ok || pub == nil {
		return
	}
	_ = pub.PublishWorkerProgress(ctx, workerJobID, snap, checkpoint)
}

func (m *Manager) workerJob(_ context.Context, workerJobID string) (*api.WorkerTask, bool) {
	if m == nil || m.workerQueue == nil {
		return nil, false
	}
	return m.workerQueue.Get(strings.TrimSpace(workerJobID))
}
