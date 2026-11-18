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
func (q *sqlQueuePromotion) SetChildSessionID(ctx context.Context, jobID, childSessionID string) error {
	if err := q.store.SetChildSessionID(ctx, jobID, childSessionID); err != nil {
		return err
	}
	q.changes.byID(ctx, jobID)
	return nil
}

// PublishWorkerProgress checkpoints round boundaries.
func (q *sqlQueuePromotion) PublishWorkerProgress(ctx context.Context, workerJobID string, snap workerprogress.Snapshot, checkpoint bool) error {
	if q == nil || q.sqlQueueCore == nil || q.store == nil {
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
	q.changes.board(ctx, *task, task.ProjectID)
	return nil
}
