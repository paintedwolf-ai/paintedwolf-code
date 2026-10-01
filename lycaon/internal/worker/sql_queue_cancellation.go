package worker

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// Cancel aborts or settles a canceled worker.
func (q *SQLQueue) Cancel(ctx context.Context, jobID string, result *api.WorkerResult) error {
	if err := q.StopExecution(ctx, jobID); err != nil {
		return err
	}
	return q.FinishCanceled(ctx, jobID, result)
}

// StopExecution retains the workspace until runtime has released it.
func (q *SQLQueue) StopExecution(ctx context.Context, jobID string) error {
	task, ok := q.Get(jobID) //nolint:contextcheck // Get has internal sqlQueueGetTimeout
	if !ok {
		return fmt.Errorf("job not found: %s", jobID)
	}
	if task.Status.IsTerminal() {
		return nil
	}
	requested, err := q.RequestCancellation(ctx, jobID)
	if err != nil || !requested {
		return err
	}
	// Re-read after fencing: a pending task may have been claimed meanwhile.
	task, ok = q.Get(jobID) //nolint:contextcheck // Get has internal sqlQueueGetTimeout
	if !ok {
		return fmt.Errorf("job not found: %s", jobID)
	}
	var interruptErr error
	if task.ChildSessionID != "" || task.Status == api.WorkerStatusRunning || task.Status == api.WorkerStatusWaiting {
		q.mu.Lock()
		onCancel := q.onRunningCancel
		q.mu.Unlock()
		if onCancel != nil {
			interruptErr = onCancel(ctx, jobID)
		}
	}
	if interruptErr != nil {
		return interruptErr
	}
	return nil
}

// RequestCancellation durably fences claims and execution outcomes before joining runtime.
func (q *SQLQueue) RequestCancellation(ctx context.Context, jobID string) (bool, error) {
	n, err := q.store.queries.RequestWorkerCancellation(ctx, db.RequestWorkerCancellationParams{
		ID: jobID, RequestedAt: db.NullString(db.FormatTime(time.Now().UTC())),
	})
	return n == 1, err
}

// RequestClaimCancellation preserves a stopped attempt when runtime cleanup must be retried.
func (q *SQLQueue) RequestClaimCancellation(ctx context.Context, claimed *api.WorkerTask) (bool, error) {
	if claimed == nil || claimed.ClaimToken == "" {
		return false, fmt.Errorf("claimed worker task required")
	}
	var won bool
	err := q.store.inTx(ctx, func(queries *db.Queries, _ *sql.Tx) error {
		job, err := queries.GetWorkerJob(ctx, claimed.ID)
		if db.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if job.Status != "running" || db.StringFromNull(job.ClaimToken) != claimed.ClaimToken || job.CancelRequestedAt.Valid {
			return nil
		}
		n, err := queries.RequestWorkerCancellation(ctx, db.RequestWorkerCancellationParams{
			ID: claimed.ID, RequestedAt: db.NullString(db.FormatTime(time.Now().UTC())),
		})
		won = n == 1
		return err
	})
	return won, err
}

// FinishCanceled records cancellation after execution exits.
func (q *SQLQueue) FinishCanceled(ctx context.Context, jobID string, result *api.WorkerResult) error {
	if result == nil {
		q.mu.Lock()
		deps := q.cancelReports
		q.mu.Unlock()
		result = cancellationResultForJob(ctx, q, jobID, deps)
	}
	return q.finalizeCanceled(ctx, jobID, result)
}

// Hold moves a pending job to held without terminating it.
func (q *SQLQueue) Hold(ctx context.Context, jobID string) error {
	task, ok := q.Get(jobID) //nolint:contextcheck // Get has internal sqlQueueGetTimeout
	if !ok {
		return fmt.Errorf("job not found: %s", jobID)
	}
	if task.Status != api.WorkerStatusPending {
		return nil
	}
	held, err := q.store.HoldJob(ctx, jobID)
	if err != nil {
		return err
	}
	if held {
		q.refreshBoardByID(ctx, jobID)
	}
	return nil
}

func (q *SQLQueue) finalizeCanceled(ctx context.Context, jobID string, result *api.WorkerResult) error {
	// Read the workspace before the cancel clears it.
	task, ok := q.Get(jobID) //nolint:contextcheck // Get has internal sqlQueueGetTimeout
	if !ok {
		return fmt.Errorf("job not found: %s", jobID)
	}
	if task.Status.IsTerminal() {
		return nil
	}
	won, err := q.store.CancelJob(ctx, jobID, result)
	if err != nil {
		return err
	}
	if !won {
		return nil
	}
	q.destroyWorkerWorkspace(ctx, task)
	q.refreshBoardByID(ctx, jobID)
	return nil
}
