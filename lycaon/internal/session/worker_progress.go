package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/workerprogress"
)

type workerProgressPublisher interface {
	PublishWorkerProgress(ctx context.Context, workerJobID string, snap workerprogress.Snapshot, checkpoint bool) error
}
