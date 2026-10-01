package worker

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/pkg/api"
)

// ClaimNext claims the next matching pending job.
func (q *InMemoryQueue) ClaimNext(ctx context.Context, req ClaimRequest) (*api.WorkerTask, error) {
	if _, err := q.recoverPrerequisites(ctx); err != nil {
		return nil, err
	}
	for {
		candidate, err := q.nextPending(ctx, req)
		if err != nil {
			return nil, err
		}
		var task *api.WorkerTask
		err = q.WithTaskAdmission(ctx, *candidate, func() error {
			var err error
			task, err = q.claimPending(ctx, req, candidate.ID)
			return err
		})
		if errors.Is(err, ErrNoPendingJobs) {
			continue
		}
		return task, err
	}
}

func (q *InMemoryQueue) nextPending(ctx context.Context, req ClaimRequest) (*api.WorkerTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.running) >= q.maxRunning {
		return nil, ErrMaxWorkers
	}
	target := req.ExecutionTarget
	if target == "" {
		target = api.ExecutionTargetLocal
	}
	for _, id := range q.pending {
		job := q.jobs[id]
		if job == nil || job.cancelRequested || job.task.Status != api.WorkerStatusPending || job.task.ExecutionTarget != target {
			continue
		}
		if req.ProjectID != "" && job.projectID != req.ProjectID && job.task.ProjectID != req.ProjectID {
			continue
		}
		if !q.prerequisitesReady(job.task) {
			continue
		}
		copy := job.task
		return &copy, nil
	}
	return nil, ErrNoPendingJobs
}

func (q *InMemoryQueue) claimPending(ctx context.Context, req ClaimRequest, expectedID string) (*api.WorkerTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q.mu.Lock()

	if len(q.running) >= q.maxRunning {
		q.mu.Unlock()
		return nil, ErrMaxWorkers
	}

	target := req.ExecutionTarget
	if target == "" {
		target = api.ExecutionTargetLocal
	}

	for i, id := range q.pending {
		if id != expectedID {
			continue
		}
		job, ok := q.jobs[id]
		if !ok || job.task.Status != api.WorkerStatusPending || job.cancelRequested {
			continue
		}
		if job.task.Status == api.WorkerStatusHeld {
			continue
		}
		if job.task.ExecutionTarget != target {
			continue
		}
		if req.ProjectID != "" && job.projectID != req.ProjectID && job.task.ProjectID != req.ProjectID {
			continue
		}
		if !q.prerequisitesReady(job.task) {
			continue
		}
		q.pending = append(q.pending[:i], q.pending[i+1:]...)
		now := time.Now().UTC()
		job.task.Status = api.WorkerStatusRunning
		job.task.StartedAt = &now
		job.task.ClaimToken = uuid.NewString()
		job.task.Attempt++
		job.task.HeartbeatAt = &now
		leaseExpiresAt := now.Add(workerClaimLease)
		job.task.LeaseExpiresAt = &leaseExpiresAt
		if req.ClaimedBy != "" {
			job.task.ClaimedBy = req.ClaimedBy
		}
		q.running[id] = struct{}{}
		out := job.task
		projectID := job.projectID
		q.mu.Unlock()
		q.refreshBoard(ctx, out, projectID)
		return &out, nil
	}
	q.mu.Unlock()
	return nil, ErrNoPendingJobs
}

func (q *InMemoryQueue) RenewClaim(ctx context.Context, jobID, claimToken string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	job, ok := q.jobs[jobID]
	if !ok || job.task.Status != api.WorkerStatusRunning || job.task.ClaimToken != claimToken || job.cancelRequested {
		return false, nil
	}
	now := time.Now().UTC()
	job.task.HeartbeatAt = &now
	leaseExpiresAt := now.Add(workerClaimLease)
	job.task.LeaseExpiresAt = &leaseExpiresAt
	return true, nil
}

// RecoverExpiredClaims retries expired attempts while runway remains.
func (q *InMemoryQueue) RecoverExpiredClaims(ctx context.Context) ([]api.WorkerTask, error) {
	prerequisites, prerequisiteErr := q.recoverPrerequisites(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	canceled, cancelErr := q.recoverCancellations(ctx)
	canceled = append(prerequisites, canceled...)
	cancelErr = errors.Join(prerequisiteErr, cancelErr)
	q.mu.Lock()
	now := time.Now().UTC()
	var ids []string
	for id, job := range q.jobs {
		if !job.cancelRequested && job.task.Status == api.WorkerStatusRunning && job.task.LeaseExpiresAt != nil && !job.task.LeaseExpiresAt.After(now) {
			ids = append(ids, id)
		}
	}
	q.mu.Unlock()
	recovered, err := recoverExpiredWorkerJobs(ctx, q, ids)
	return append(canceled, recovered...), errors.Join(cancelErr, err)
}

func (q *InMemoryQueue) recoverCancellations(ctx context.Context) ([]api.WorkerTask, error) {
	q.mu.Lock()
	now := time.Now()
	var ids []string
	for id, job := range q.jobs {
		expired := job.task.Status == api.WorkerStatusRunning && (job.task.LeaseExpiresAt == nil || !job.task.LeaseExpiresAt.After(now))
		inactive := job.task.Status == api.WorkerStatusPending || job.task.Status == api.WorkerStatusWaiting || job.task.Status == api.WorkerStatusHeld
		if job.cancelRequested && (expired || inactive) {
			ids = append(ids, id)
		}
	}
	q.mu.Unlock()
	return recoverCancellationJobs(ctx, q, ids)
}
