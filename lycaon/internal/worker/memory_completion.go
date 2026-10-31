package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

// Retry closes a failed attempt without making the worker job terminal.
func (q *memoryQueueClaims) Retry(ctx context.Context, claimed *api.WorkerTask, runErr error) (bool, error) {
	return admitWorkerTransition(ctx, q.self, claimed, func() (bool, error) {
		return q.retryAdmitted(ctx, claimed, runErr)
	})
}

func (q *memoryQueueClaims) retryAdmitted(ctx context.Context, claimed *api.WorkerTask, runErr error) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if claimed == nil || strings.TrimSpace(claimed.ID) == "" || strings.TrimSpace(claimed.ClaimToken) == "" {
		return false, fmt.Errorf("claimed worker task required")
	}
	q.mu.Lock()
	job, ok := q.jobs[claimed.ID]
	if !ok {
		q.mu.Unlock()
		return false, fmt.Errorf("job not found: %s", claimed.ID)
	}
	if job.task.Status != api.WorkerStatusRunning || job.task.ClaimToken != claimed.ClaimToken || job.cancelRequested {
		q.mu.Unlock()
		return false, nil
	}
	job.task.Status = api.WorkerStatusPending
	job.task.ClaimedBy = ""
	job.task.ClaimToken = ""
	job.task.HeartbeatAt = nil
	job.task.LeaseExpiresAt = nil
	if runErr != nil {
		job.task.Error = runErr.Error()
	}
	delete(q.running, claimed.ID)
	q.pending = append(q.pending, claimed.ID)
	snapshot := job.task
	projectID := job.projectID
	q.mu.Unlock()
	q.refreshBoard(ctx, snapshot, projectID)
	q.NotifyRunnable()
	return true, nil
}

func (q *memoryQueueClaims) Park(ctx context.Context, claimed *api.WorkerTask) (bool, error) {
	return admitWorkerTransition(ctx, q.self, claimed, func() (bool, error) {
		return q.parkAdmitted(ctx, claimed)
	})
}

func (q *memoryQueueClaims) parkAdmitted(ctx context.Context, claimed *api.WorkerTask) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	job, ok := q.jobs[claimed.ID]
	if !ok || job.task.Status != api.WorkerStatusRunning || job.task.ClaimToken != claimed.ClaimToken || job.cancelRequested {
		return false, nil
	}
	job.task.Status = api.WorkerStatusWaiting
	job.task.ClaimedBy, job.task.ClaimToken = "", ""
	job.task.HeartbeatAt, job.task.LeaseExpiresAt = nil, nil
	delete(q.running, claimed.ID)
	return true, nil
}

func (q *memoryQueueClaims) ResumeReadyWaits(context.Context) (int, error) { return 0, nil }

// Complete marks a job completed.
func (q *memoryQueueClaims) Complete(ctx context.Context, claimed *api.WorkerTask, result api.WorkerResult) (bool, error) {
	return admitWorkerTransition(ctx, q.self, claimed, func() (bool, error) {
		return q.completeAdmitted(ctx, claimed, result)
	})
}

func (q *memoryQueueClaims) completeAdmitted(ctx context.Context, claimed *api.WorkerTask, result api.WorkerResult) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if claimed == nil || strings.TrimSpace(claimed.ID) == "" {
		return false, fmt.Errorf("claimed worker task required")
	}
	jobID := claimed.ID
	q.mu.Lock()

	job, ok := q.jobs[jobID]
	if !ok {
		q.mu.Unlock()
		return false, fmt.Errorf("job not found: %s", jobID)
	}
	if job.task.Status.IsTerminal() {
		q.mu.Unlock()
		return false, nil
	}
	if job.task.Status != api.WorkerStatusRunning ||
		strings.TrimSpace(claimed.ClaimToken) == "" ||
		job.task.ClaimToken != claimed.ClaimToken || job.cancelRequested {
		q.mu.Unlock()
		return false, nil
	}
	now := time.Now().UTC()
	job.task.Status = api.WorkerStatusComplete
	if strings.TrimSpace(result.Status) == "needs_decision" {
		job.task.Status = api.WorkerStatusHeld
	}
	job.task.Result = &result
	job.task.CompletedAt = &now
	if job.task.EffectiveScope().IsWrite() {
		if strings.TrimSpace(result.Status) == "needs_decision" {
			job.task.MergeStatus = ""
		} else if strings.TrimSpace(job.task.WorkspaceRoot) != "" {
			job.task.MergeStatus = api.WorkerMergeStatusPending
		} else {
			job.task.MergeStatus = api.WorkerMergeStatusAborted
		}
	}
	delete(q.running, jobID)
	snapshot := job.task
	projectID := job.projectID
	projects := q.projects
	q.mu.Unlock()
	snapshot, err := q.sealCompletedMemoryOverlay(ctx, jobID, snapshot, projects)
	if err != nil {
		return false, err
	}
	q.refreshBoard(ctx, snapshot, projectID)
	return true, nil
}

func (q *memoryQueueClaims) sealCompletedMemoryOverlay(ctx context.Context, jobID string, snapshot api.WorkerTask, projects ProjectStore) (api.WorkerTask, error) {
	if snapshot.MergeStatus != api.WorkerMergeStatusPending || strings.TrimSpace(snapshot.WorkspaceOverlayPath) != "" || strings.TrimSpace(snapshot.WorkspaceBaselinePath) == "" {
		return snapshot, nil
	}
	overlayPath, err := captureOverlay(ctx, &snapshot, projects)
	if err != nil {
		if errors.Is(err, workspacebaseline.ErrOverlayBudgetExceeded) {
			workerWorkspaceLog.Warn("write overlay exceeds budget; leaving unsealed for review",
				"job_id", jobID, "error", err)
			return snapshot, nil
		}
		return snapshot, err
	}
	q.mu.Lock()
	if j, ok := q.jobs[jobID]; ok && j != nil {
		j.task.WorkspaceOverlayPath = overlayPath
		snapshot = j.task
	}
	q.mu.Unlock()
	return snapshot, nil
}

// Fail commits a failure only while the claim remains current.
func (q *memoryQueueClaims) Fail(ctx context.Context, claimed *api.WorkerTask, runErr error) (bool, error) {
	return admitWorkerTransition(ctx, q.self, claimed, func() (bool, error) {
		return q.failAdmitted(ctx, claimed, runErr)
	})
}

func (q *memoryQueueClaims) failAdmitted(ctx context.Context, claimed *api.WorkerTask, runErr error) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if claimed == nil || strings.TrimSpace(claimed.ID) == "" || strings.TrimSpace(claimed.ClaimToken) == "" {
		return false, fmt.Errorf("claimed worker task required")
	}
	jobID := claimed.ID
	q.mu.Lock()
	job, ok := q.jobs[jobID]
	if !ok {
		q.mu.Unlock()
		return false, fmt.Errorf("job not found: %s", jobID)
	}
	if job.task.Status != api.WorkerStatusRunning || job.task.ClaimToken != claimed.ClaimToken || job.cancelRequested {
		q.mu.Unlock()
		return false, nil
	}
	workspaceRoot := strings.TrimSpace(job.task.WorkspaceRoot)
	now := time.Now().UTC()
	job.task.Status = api.WorkerStatusFailed
	if runErr != nil {
		job.task.Error = runErr.Error()
		if q.failureCatalog != nil {
			failure := q.failureCatalog.RenderExecuteFailure(runErr)
			job.task.Failure = &failure
		}
	}
	job.task.CompletedAt = &now
	job.task.MergeStatus = api.WorkerMergeStatusAborted
	job.task.WorkspaceRoot = ""
	delete(q.running, jobID)
	snapshot := job.task
	projectID := job.projectID
	q.mu.Unlock()
	destroyWorkspaceRoot(q.self, jobID, workspaceRoot)
	q.refreshBoard(ctx, snapshot, projectID)
	return true, nil
}
