package worker

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// Enqueue adds a worker task.
func (q *sqlQueueClaims) Enqueue(ctx context.Context, task api.WorkerTask) (string, error) {
	return q.EnqueueWithProjectID(ctx, "", task)
}

// EnqueueWithProjectID enqueues a task scoped to a project id.
func (q *sqlQueueClaims) EnqueueWithProjectID(ctx context.Context, projectID string, task api.WorkerTask) (string, error) {
	var jobID string
	err := q.WithTaskAdmission(ctx, task, func() error {
		var enqueueErr error
		jobID, enqueueErr = q.enqueueAdmitted(ctx, projectID, task)
		return enqueueErr
	})
	return jobID, err
}

func (q *sqlQueueClaims) enqueueAdmitted(ctx context.Context, projectID string, task api.WorkerTask) (string, error) {
	if current, ok := q.idempotentTask(ctx, task); ok {
		err := compareTaskReceipt(current, task)
		if err == nil && current.Status == api.WorkerStatusPending {
			q.NotifyRunnable()
		}
		return current.ID, err
	}
	if err := q.PrepareEnqueue(ctx, projectID, &task); err != nil {
		return "", err
	}
	if err := q.store.InsertTask(ctx, task); err != nil {
		if current, ok := q.idempotentTask(ctx, task); ok {
			receiptErr := compareTaskReceipt(current, task)
			if receiptErr == nil && current.Status == api.WorkerStatusPending {
				q.NotifyRunnable()
			}
			return current.ID, receiptErr
		}
		return "", err
	}
	q.refreshBoard(ctx, task, task.ProjectID)
	q.NotifyRunnable()
	return task.ID, nil
}

func (q *sqlQueueClaims) idempotentTask(ctx context.Context, task api.WorkerTask) (*api.WorkerTask, bool) {
	if strings.TrimSpace(task.ParentSessionID) == "" || strings.TrimSpace(task.SourceToolCallID) == "" {
		return nil, false
	}
	current, err := q.TaskReceipt(ctx, task.ParentSessionID, task.SourceToolCallID)
	return current, err == nil && current != nil
}

func compareTaskReceipt(current *api.WorkerTask, proposed api.WorkerTask) error {
	if current != nil && current.SourceArgsDigest == proposed.SourceArgsDigest && proposed.SourceArgsDigest != "" {
		return nil
	}
	return fmt.Errorf("task tool call %s was already used for different arguments", proposed.SourceToolCallID)
}

// PrepareEnqueue applies queue invariants without writing.
func (q *sqlQueueClaims) PrepareEnqueue(ctx context.Context, projectID string, task *api.WorkerTask) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("worker task required")
	}
	q.mu.Lock()
	defaultTarget := q.defaultTarget
	q.mu.Unlock()
	if task.ExecutionTarget == "" {
		task.ExecutionTarget = defaultTarget
	}
	scope := enqueueScope(projectID, task)
	if err := ApplyEnqueueDefaults(task, scope, DefaultWorkersConfig()); err != nil {
		return err
	}
	if err := validatePrerequisites(ctx, *task, q.Get); err != nil {
		return err
	}
	if task.WorkflowRunID != "" {
		// Workflow tasks require a runnable-state source.
		if q.workflowRuns == nil || q.workflowRuns.Runs == nil {
			return fmt.Errorf("worker queue: workflow run checker required for workflow-bound task %s", task.WorkflowRunID)
		}
		if err := q.workflowRuns.Runs.AssertRunnable(ctx, task.WorkflowRunID); err != nil {
			return err
		}
		if task.WorkflowPhase != "" {
			if q.workflowRuns.Tasks == nil {
				return fmt.Errorf("worker queue: workflow task admission required for phase-bound task %s", task.WorkflowPhase)
			}
			if err := q.workflowRuns.Tasks.AssertWorkerTask(ctx, task); err != nil {
				return err
			}
		}
	}
	if task.ID == "" {
		task.ID = uuid.NewString()
	}
	if task.EffectiveScope().IsWrite() && strings.TrimSpace(task.OverlayID) == "" {
		task.OverlayID = task.ID
	}
	return nil
}

// PublishEnqueued refreshes projections and wakes claimers.
func (q *sqlQueueClaims) PublishEnqueued(ctx context.Context, jobID string) {
	q.refreshBoardByID(ctx, jobID)
	q.NotifyRunnable()
}

// ClaimNext claims the next pending job when under the running cap.
func (q *sqlQueueClaims) ClaimNext(ctx context.Context, req ClaimRequest) (*api.WorkerTask, error) {
	if _, err := q.recoverPrerequisites(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q.mu.Lock()
	maxRunning := q.maxRunning
	q.mu.Unlock()
	task, err := q.store.ClaimNext(ctx, req, maxRunning, q.self)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("worker queue: claim returned no task")
	}
	q.refreshBoard(ctx, *task, task.ProjectID)
	return task, nil
}

// Cancel aborts or settles a canceled worker.
func (q *sqlQueueClaims) Cancel(ctx context.Context, jobID string, result *api.WorkerResult) error {
	if err := q.StopExecution(ctx, jobID); err != nil {
		return err
	}
	return q.FinishCanceled(ctx, jobID, result)
}

// StopExecution retains the workspace until runtime has released it.
func (q *sqlQueueClaims) StopExecution(ctx context.Context, jobID string) error {
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
func (q *sqlQueueClaims) RequestCancellation(ctx context.Context, jobID string) (bool, error) {
	n, err := q.store.queries.RequestWorkerCancellation(ctx, db.RequestWorkerCancellationParams{
		ID: jobID, RequestedAt: db.NullString(db.FormatTime(time.Now().UTC())),
	})
	return n == 1, err
}

// RequestClaimCancellation preserves a stopped attempt when runtime cleanup must be retried.
func (q *sqlQueueClaims) RequestClaimCancellation(ctx context.Context, claimed *api.WorkerTask) (bool, error) {
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
func (q *sqlQueueClaims) FinishCanceled(ctx context.Context, jobID string, result *api.WorkerResult) error {
	if result == nil {
		q.mu.Lock()
		deps := q.cancelReports
		q.mu.Unlock()
		result = cancellationResultForJob(ctx, q.self, jobID, deps)
	}
	return q.finalizeCanceled(ctx, jobID, result)
}

// Hold moves a pending job to held without terminating it.
func (q *sqlQueueClaims) Hold(ctx context.Context, jobID string) error {
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

func (q *sqlQueueClaims) finalizeCanceled(ctx context.Context, jobID string, result *api.WorkerResult) error {
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
