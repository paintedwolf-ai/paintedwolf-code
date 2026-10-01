package worker

import (
	"context"
	"database/sql"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/workerprogress"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetProgress checkpoints a worker's lifetime progress counters on its job row.
func (s *SQLStore) SetProgress(ctx context.Context, workerJobID string, snap workerprogress.Snapshot) error {
	workerJobID = strings.TrimSpace(workerJobID)
	if workerJobID == "" {
		return nil
	}
	return s.inTx(ctx, func(q *db.Queries, tx *sql.Tx) error {
		if err := q.SetWorkerJobProgress(ctx, db.SetWorkerJobProgressParams{
			ToolLoopsUsed: sql.NullInt64{Int64: int64(snap.ToolLoopsUsed), Valid: true},
			ToolCallsUsed: sql.NullInt64{Int64: int64(snap.ToolCallsUsed), Valid: true},
			ID:            workerJobID,
		}); err != nil {
			return err
		}
		row, err := q.GetWorkerJob(ctx, workerJobID)
		if db.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		return s.emitJobTx(ctx, tx, row.ID)
	})
}

// SetChildSessionID binds the spawned child session to a worker job.
func (q *SQLQueue) SetChildSessionID(ctx context.Context, jobID, childSessionID string) error {
	if err := q.store.SetChildSessionID(ctx, jobID, childSessionID); err != nil {
		return err
	}
	q.refreshBoardByID(ctx, jobID)
	return nil
}

// PublishWorkerProgress checkpoints round boundaries.
func (q *SQLQueue) PublishWorkerProgress(ctx context.Context, workerJobID string, snap workerprogress.Snapshot, checkpoint bool) error {
	if q == nil || q.store == nil {
		return nil
	}
	workerJobID = strings.TrimSpace(workerJobID)
	if workerJobID == "" {
		return nil
	}
	task, ok := q.store.getTask(ctx, workerJobID)
	if !ok || task == nil {
		return nil
	}
	workerprogress.Apply(task, snap)
	if checkpoint {
		if err := q.store.SetProgress(ctx, workerJobID, workerprogress.FromTask(task)); err != nil {
			return err
		}
	}
	q.refreshBoard(ctx, *task, task.ProjectID)
	return nil
}

// refreshBoard rebuilds the session board after a job transition.
func (q *SQLQueue) refreshBoard(ctx context.Context, task api.WorkerTask, projectID string) {
	if q.events == nil {
		return
	}
	key := strings.TrimSpace(task.ProjectID)
	if key == "" {
		key = strings.TrimSpace(projectID)
	}
	q.events.PublishBoard(ctx, key, strings.TrimSpace(task.ParentSessionID))
}

func (q *SQLQueue) refreshBoardByID(ctx context.Context, jobID string) {
	if task, ok := q.Get(jobID); ok { //nolint:contextcheck // Get has internal sqlQueueGetTimeout
		q.refreshBoard(ctx, *task, task.ProjectID)
	}
}
