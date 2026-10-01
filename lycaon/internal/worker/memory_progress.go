package worker

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/workerprogress"
)

// SetChildSessionID binds the spawned child session to a worker job.
func (q *InMemoryQueue) SetChildSessionID(ctx context.Context, jobID, childSessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q.mu.Lock()
	job, ok := q.jobs[jobID]
	if !ok {
		q.mu.Unlock()
		return fmt.Errorf("job not found: %s", jobID)
	}
	job.task.ChildSessionID = strings.TrimSpace(childSessionID)
	snapshot := job.task
	projectID := job.projectID
	q.mu.Unlock()
	q.refreshBoard(ctx, snapshot, projectID)
	return nil
}

// PublishWorkerProgress emits one progress edge.
func (q *InMemoryQueue) PublishWorkerProgress(ctx context.Context, workerJobID string, snap workerprogress.Snapshot, _ bool) error {
	if q == nil {
		return nil
	}
	workerJobID = strings.TrimSpace(workerJobID)
	if workerJobID == "" {
		return nil
	}
	q.mu.Lock()
	job := q.jobs[workerJobID]
	if job == nil {
		q.mu.Unlock()
		return nil
	}
	workerprogress.Apply(&job.task, snap)
	snapshot := job.task
	projectID := job.projectID
	q.mu.Unlock()
	q.refreshBoard(ctx, snapshot, projectID)
	return nil
}
