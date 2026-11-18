package worker

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/pkg/api"
)

// jobChanges publishes derived board state after durable job transitions.
type jobChanges struct {
	store     *SQLStore
	publisher *events.Publisher
}

func (c *jobChanges) board(ctx context.Context, task api.WorkerTask, projectID string) {
	if c.publisher == nil {
		return
	}
	key := strings.TrimSpace(task.ProjectID)
	if key == "" {
		key = strings.TrimSpace(projectID)
	}
	c.publisher.PublishBoard(ctx, key, strings.TrimSpace(task.ParentSessionID))
}

func (c *jobChanges) byID(ctx context.Context, jobID string) {
	// Read the committed job even if its caller has canceled.
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sqlQueueGetTimeout)
	defer cancel()
	if task, ok := c.store.getTask(lookupCtx, jobID); ok {
		c.board(ctx, *task, task.ProjectID)
	}
}
