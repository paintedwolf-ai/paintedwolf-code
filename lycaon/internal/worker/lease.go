package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/pkg/api"
)

const workerClaimLease = 45 * time.Second
const workerClaimExpiredError = "worker interrupted after its host lease expired"

var ErrClaimLost = fmt.Errorf("worker claim lost")

func (s *SQLStore) RenewClaim(ctx context.Context, jobID, claimToken string) (bool, error) {
	now := time.Now().UTC()
	rows, err := s.queries.RenewWorkerClaim(ctx, db.RenewWorkerClaimParams{
		HeartbeatAt: db.NullString(db.FormatTime(now)), LeaseExpiresAt: db.NullString(db.FormatTime(now.Add(workerClaimLease))),
		ID: jobID, ClaimToken: db.NullString(claimToken),
	})
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

// Lease recovery uses ordinary queue transitions, including session admission.
func recoverExpiredWorkerJobs(ctx context.Context, queue WorkerQueue, ids []string) ([]api.WorkerTask, error) {
	var recovered []api.WorkerTask
	var errs []error
	for _, id := range ids {
		task, ok := queue.Get(id)
		if !ok || task.Status != api.WorkerStatusRunning || isJobCancellationRequested(ctx, queue, id) || task.LeaseExpiresAt == nil || task.LeaseExpiresAt.After(time.Now()) {
			continue
		}
		var won bool
		var err error
		if task.Attempt < maxWorkerExecutionAttempts {
			won, err = queue.Retry(ctx, task, errors.New(workerClaimExpiredError))
		} else {
			won, err = queue.Fail(ctx, task, errors.New(workerClaimExpiredError))
		}
		if errors.Is(err, lifecycle.ErrStopping) {
			won, err = queue.RequestClaimCancellation(ctx, task)
			if err == nil && won {
				stopCtx, cancel := context.WithTimeout(ctx, workerOutcomeDeliveryLimit)
				err = queue.Cancel(stopCtx, id, nil)
				cancel()
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("recover worker %s claim: %w", id, err))
			continue
		}
		if won {
			if current, ok := queue.Get(id); ok && (current.Status == api.WorkerStatusFailed || current.Status == api.WorkerStatusCanceled) {
				recovered = append(recovered, *current)
			}
		}
	}
	return recovered, errors.Join(errs...)
}

// A persisted stop still joins any surviving local runtime before cleanup.
func (q *sqlQueueClaims) recoverCancellations(ctx context.Context) ([]api.WorkerTask, error) {
	ids, err := q.store.queries.ListInterruptedWorkerCancellationIDs(ctx, db.NullString(db.FormatTime(time.Now().UTC())))
	if err != nil {
		return nil, err
	}
	return recoverCancellationJobs(ctx, q.self, ids)
}

func recoverCancellationJobs(ctx context.Context, queue WorkerQueue, ids []string) ([]api.WorkerTask, error) {
	var recovered []api.WorkerTask
	var errs []error
	for _, id := range ids {
		// An active local execution settles its own cancellation when it exits.
		if queueHasActiveExecution(queue, id) {
			continue
		}
		stopCtx, cancel := context.WithTimeout(ctx, workerOutcomeDeliveryLimit)
		err := queue.Cancel(stopCtx, id, nil)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("recover worker %s cancellation: %w", id, err))
			continue
		}
		if task, ok := queue.Get(id); ok && task.Status == api.WorkerStatusCanceled {
			recovered = append(recovered, *task)
		}
	}
	return recovered, errors.Join(errs...)
}

func setQueueActiveExecution(queue WorkerQueue, fn func(jobID string) bool) {
	switch q := queue.(type) {
	case *SQLQueue:
		q.mu.Lock()
		q.activeExecution = fn
		q.mu.Unlock()
	case *InMemoryQueue:
		q.mu.Lock()
		q.activeExecution = fn
		q.mu.Unlock()
	}
}

func queueHasActiveExecution(queue WorkerQueue, jobID string) bool {
	var fn func(string) bool
	switch q := queue.(type) {
	case *SQLQueue:
		if q != nil {
			q.mu.Lock()
			fn = q.activeExecution
			q.mu.Unlock()
		}
	case *InMemoryQueue:
		if q != nil {
			q.mu.Lock()
			fn = q.activeExecution
			q.mu.Unlock()
		}
	}
	return fn != nil && fn(jobID)
}

func isJobCancellationRequested(ctx context.Context, queue WorkerQueue, jobID string) bool {
	switch q := queue.(type) {
	case *InMemoryQueue:
		if q == nil {
			return false
		}
		q.mu.Lock()
		job, ok := q.jobs[jobID]
		requested := ok && (job.cancelRequested || job.task.Status == api.WorkerStatusCanceled)
		q.mu.Unlock()
		return requested
	case *SQLQueue:
		if q == nil || q.store == nil {
			return false
		}
		fenceCtx, cancelFence := cancellationFenceContext(ctx)
		defer cancelFence()
		queryCtx, cancel := context.WithTimeout(fenceCtx, sqlQueueGetTimeout)
		defer cancel()
		row, err := q.store.queries.GetWorkerJob(queryCtx, jobID)
		if err != nil {
			return false
		}
		return row.CancelRequestedAt.Valid || row.Status == string(api.WorkerStatusCanceled)
	default:
		if queue == nil {
			return false
		}
		cur, ok := queue.Get(jobID)
		return ok && cur != nil && cur.Status == api.WorkerStatusCanceled
	}
}

func (q *sqlQueueClaims) RenewClaim(ctx context.Context, jobID, claimToken string) (bool, error) {
	return q.store.RenewClaim(ctx, jobID, claimToken)
}

// RecoverExpiredClaims tears down workspaces for terminal recoveries.
func (q *sqlQueueClaims) RecoverExpiredClaims(ctx context.Context) ([]api.WorkerTask, error) {
	prerequisites, prerequisiteErr := q.recoverPrerequisites(ctx)
	canceled, cancelErr := q.recoverCancellations(ctx)
	canceled = append(prerequisites, canceled...)
	cancelErr = errors.Join(prerequisiteErr, cancelErr)
	ids, err := q.store.queries.ListExpiredWorkerJobIDs(ctx, db.NullString(db.FormatTime(time.Now().UTC())))
	if err != nil {
		return canceled, errors.Join(cancelErr, err)
	}
	recovered, err := recoverExpiredWorkerJobs(ctx, q.self, ids)
	return append(canceled, recovered...), errors.Join(cancelErr, err)
}

var _ WorkerQueue = (*SQLQueue)(nil)
