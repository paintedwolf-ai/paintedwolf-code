package worker

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/workspace"
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
	var n int64
	err := q.retryOnFullDisk(func() (err error) {
		n, err = q.store.queries.RequestWorkerCancellation(ctx, db.RequestWorkerCancellationParams{
			ID: jobID, RequestedAt: db.NullString(db.FormatTime(time.Now().UTC())),
		})
		return err
	})
	return n == 1, err
}

// SpaceReserveReleaser frees room set aside for finishing a cancel on a full volume.
type SpaceReserveReleaser interface {
	ReleaseSpaceReserve() bool
}

var _ SpaceReserveReleaser = (*workspace.Manager)(nil)

// retryOnFullDisk gives a cancellation write one more try with the reserve freed. A worker that
// filled the volume the database shares must still be cancelable, or nothing can free the space.
func (q *SQLQueue) retryOnFullDisk(write func() error) error {
	err := write()
	if !db.IsDiskFull(err) {
		return err
	}
	q.mu.Lock()
	ws := q.workerWorkspace
	q.mu.Unlock()
	if releaser, ok := ws.(SpaceReserveReleaser); ok && releaser.ReleaseSpaceReserve() {
		return write()
	}
	return err
}

// RequestClaimCancellation preserves a stopped attempt when runtime cleanup must be retried.
func (q *SQLQueue) RequestClaimCancellation(ctx context.Context, claimed *api.WorkerTask) (bool, error) {
	if claimed == nil || claimed.ClaimToken == "" {
		return false, fmt.Errorf("claimed worker task required")
	}
	var won bool
	err := q.retryOnFullDisk(func() error {
		return q.store.inTx(ctx, func(queries *db.Queries, _ *sql.Tx) error {
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
	})
	return won, err
}

// WorkerRuntimeSettler proves a worker's view and writers are gone before cancellation commits.
type WorkerRuntimeSettler interface {
	SettleBranchRuntime(ctx context.Context, jobID, root string) error
}

// CanceledWorkRetainer keeps a canceled worker's unpromoted bytes before its tree is deleted.
type CanceledWorkRetainer interface {
	RetainCanceledWork(ctx context.Context, jobID, root, baselinePath string) error
}

var _ CanceledWorkRetainer = (*workspace.Manager)(nil)

// settleCanceledRuntime runs on every cancellation path because each one finishes here. On
// failure the job stays cancel-requested, so maintenance retries it and its data stays put.
func (q *SQLQueue) settleCanceledRuntime(ctx context.Context, jobID string) error {
	task, ok := q.Get(jobID) //nolint:contextcheck // Get has internal sqlQueueGetTimeout
	if !ok || task.Status.IsTerminal() || strings.TrimSpace(task.WorkspaceRoot) == "" {
		return nil
	}
	q.mu.Lock()
	ws := q.workerWorkspace
	q.mu.Unlock()
	if settler, ok := ws.(WorkerRuntimeSettler); ok {
		if err := settler.SettleBranchRuntime(ctx, jobID, task.WorkspaceRoot); err != nil {
			return err
		}
	}
	if err := workspace.RefuseMountedBranch(task.WorkspaceRoot); err != nil {
		return err
	}
	if retainer, ok := ws.(CanceledWorkRetainer); ok {
		return retainer.RetainCanceledWork(ctx, jobID, task.WorkspaceRoot, task.WorkspaceBaselinePath)
	}
	return nil
}

// FinishCanceled records cancellation after execution exits and its runtime settled.
func (q *SQLQueue) FinishCanceled(ctx context.Context, jobID string, result *api.WorkerResult) error {
	if err := q.settleCanceledRuntime(ctx, jobID); err != nil {
		return err
	}
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
	var won bool
	if err := q.retryOnFullDisk(func() (err error) {
		won, err = q.store.CancelJob(ctx, jobID, result)
		return err
	}); err != nil {
		return err
	}
	if !won {
		return nil
	}
	q.destroyWorkerWorkspace(ctx, task)
	q.refreshBoardByID(ctx, jobID)
	return nil
}
