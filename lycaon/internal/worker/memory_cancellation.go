package worker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// Cancel aborts or settles a canceled worker.
func (q *InMemoryQueue) Cancel(ctx context.Context, jobID string, result *api.WorkerResult) error {
	if err := q.StopExecution(ctx, jobID); err != nil {
		return err
	}
	return q.FinishCanceled(ctx, jobID, result)
}

// StopExecution retains the workspace until runtime has released it.
func (q *InMemoryQueue) StopExecution(ctx context.Context, jobID string) error {
	requested, err := q.RequestCancellation(ctx, jobID)
	if err != nil || !requested {
		return err
	}
	q.mu.Lock()
	job := q.jobs[jobID]
	onCancel := q.onRunningCancel
	interrupt := job.task.ChildSessionID != "" || job.task.Status == api.WorkerStatusRunning || job.task.Status == api.WorkerStatusWaiting
	q.mu.Unlock()
	if interrupt && onCancel != nil {
		if err := onCancel(ctx, jobID); err != nil {
			return err
		}
	}
	return nil
}

func (q *InMemoryQueue) RequestCancellation(ctx context.Context, jobID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	job, ok := q.jobs[jobID]
	if !ok {
		return false, fmt.Errorf("job not found: %s", jobID)
	}
	if job.task.Status.IsTerminal() {
		return false, nil
	}
	job.cancelRequested = true
	return true, nil
}

func (q *InMemoryQueue) RequestClaimCancellation(ctx context.Context, claimed *api.WorkerTask) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if claimed == nil || claimed.ClaimToken == "" {
		return false, fmt.Errorf("claimed worker task required")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	job, ok := q.jobs[claimed.ID]
	if !ok || job.task.Status != api.WorkerStatusRunning || job.task.ClaimToken != claimed.ClaimToken || job.cancelRequested {
		return false, nil
	}
	job.cancelRequested = true
	return true, nil
}

// FinishCanceled records cancellation after execution exits.
func (q *InMemoryQueue) FinishCanceled(ctx context.Context, jobID string, result *api.WorkerResult) error {
	if result == nil {
		q.mu.Lock()
		deps := q.cancelReports
		q.mu.Unlock()
		result = cancellationResultForJob(ctx, q, jobID, deps)
	}
	return finalizeCanceled(ctx, q, jobID, result)
}

// Hold moves a pending job to held without terminating it.
func (q *InMemoryQueue) Hold(ctx context.Context, jobID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q.mu.Lock()
	job, ok := q.jobs[jobID]
	if !ok {
		q.mu.Unlock()
		return fmt.Errorf("job not found: %s", jobID)
	}
	if job.task.Status != api.WorkerStatusPending || job.cancelRequested {
		q.mu.Unlock()
		return nil
	}
	job.task.Status = api.WorkerStatusHeld
	snapshot := job.task
	projectID := job.projectID
	q.mu.Unlock()
	q.refreshBoard(ctx, snapshot, projectID)
	return nil
}

func finalizeCanceled(ctx context.Context, q *InMemoryQueue, jobID string, result *api.WorkerResult) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q.mu.Lock()
	job, ok := q.jobs[jobID]
	if !ok {
		q.mu.Unlock()
		return fmt.Errorf("job not found: %s", jobID)
	}
	if job.task.Status.IsTerminal() {
		q.mu.Unlock()
		return nil
	}
	delete(q.running, jobID)
	job.task.Status = api.WorkerStatusCanceled
	if result != nil {
		copied := *result
		job.task.Result = &copied
	}
	now := time.Now().UTC()
	job.task.CompletedAt = &now
	job.task.Error = ""
	job.task.Failure = nil
	job.task.ClaimToken = ""
	job.task.HeartbeatAt = nil
	job.task.LeaseExpiresAt = nil
	if job.task.Result == nil {
		job.task.Result = &api.WorkerResult{Status: "canceled"}
	}
	workspaceRoot := strings.TrimSpace(job.task.WorkspaceRoot)
	job.task.MergeStatus = api.WorkerMergeStatusAborted
	job.task.WorkspaceRoot = ""
	snapshot := job.task
	projectID := job.projectID
	q.mu.Unlock()
	destroyWorkspaceRoot(q, jobID, workspaceRoot)
	q.refreshBoard(ctx, snapshot, projectID)
	return nil
}
