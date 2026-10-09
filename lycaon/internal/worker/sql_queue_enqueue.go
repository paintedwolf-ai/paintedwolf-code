package worker

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

// Enqueue adds a worker task.
func (q *SQLQueue) Enqueue(ctx context.Context, task api.WorkerTask) (string, error) {
	return q.EnqueueWithProjectID(ctx, "", task)
}

// EnqueueWithProjectID enqueues a task scoped to a project id.
func (q *SQLQueue) EnqueueWithProjectID(ctx context.Context, projectID string, task api.WorkerTask) (string, error) {
	var jobID string
	err := q.WithTaskAdmission(ctx, task, func() error {
		var enqueueErr error
		jobID, enqueueErr = q.enqueueAdmitted(ctx, projectID, task)
		return enqueueErr
	})
	return jobID, err
}

func (q *SQLQueue) enqueueAdmitted(ctx context.Context, projectID string, task api.WorkerTask) (string, error) {
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

func (q *SQLQueue) idempotentTask(ctx context.Context, task api.WorkerTask) (*api.WorkerTask, bool) {
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
func (q *SQLQueue) PrepareEnqueue(ctx context.Context, projectID string, task *api.WorkerTask) error {
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
func (q *SQLQueue) PublishEnqueued(ctx context.Context, jobID string) {
	q.refreshBoardByID(ctx, jobID)
	q.NotifyRunnable()
}

// ClaimNext claims the next pending job when under the running cap.
func (q *SQLQueue) ClaimNext(ctx context.Context, req ClaimRequest) (*api.WorkerTask, error) {
	if _, err := q.recoverPrerequisites(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q.mu.Lock()
	maxRunning := q.maxRunning
	q.mu.Unlock()
	task, err := q.store.ClaimNext(ctx, req, maxRunning, q)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("worker queue: claim returned no task")
	}
	q.refreshBoard(ctx, *task, task.ProjectID)
	return task, nil
}
