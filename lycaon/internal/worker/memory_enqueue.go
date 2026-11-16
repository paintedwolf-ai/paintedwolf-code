package worker

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/pkg/api"
)

func enqueueScope(projectID string, task *api.WorkerTask) project.ProjectScope {
	if task == nil {
		return project.ProjectScope{}
	}
	pid := strings.TrimSpace(task.ProjectID)
	if pid == "" {
		pid = strings.TrimSpace(projectID)
	}
	path := strings.TrimSpace(task.WorkspacePath)
	return project.ProjectScope{
		ProjectID:       pid,
		WorkspaceRootID: task.WorkspaceRootID,
		WorkspacePath:   path,
		HasRoots:        path != "",
	}
}

// Enqueue adds a worker task to the pending queue (any project when listing).
func (q *memoryQueueClaims) Enqueue(ctx context.Context, task api.WorkerTask) (string, error) {
	return q.EnqueueWithProjectID(ctx, "", task)
}

// EnqueueWithProjectID enqueues a task scoped to a project id.
func (q *memoryQueueClaims) EnqueueWithProjectID(ctx context.Context, projectID string, task api.WorkerTask) (string, error) {
	var jobID string
	err := q.WithTaskAdmission(ctx, task, func() error {
		var enqueueErr error
		jobID, enqueueErr = q.enqueueAdmitted(ctx, projectID, task)
		return enqueueErr
	})
	return jobID, err
}

func (q *memoryQueueClaims) PrepareEnqueue(ctx context.Context, projectID string, task *api.WorkerTask) error {
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
	q.mu.Lock()
	workflowRuns := q.workflowRuns
	q.mu.Unlock()
	if err := assertWorkflowTaskAdmissible(ctx, workflowRuns, task); err != nil {
		return err
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if strings.TrimSpace(task.ParentSessionID) != "" && strings.TrimSpace(task.SourceToolCallID) != "" {
		for _, current := range q.jobs {
			if current.task.ParentSessionID != task.ParentSessionID || current.task.SourceToolCallID != task.SourceToolCallID {
				continue
			}
			if err := compareTaskReceipt(&current.task, *task); err != nil {
				return err
			}
			task.ID = current.task.ID
			return nil
		}
	}

	if task.ID == "" {
		task.ID = uuid.NewString()
	}
	assignMemoryTaskDefaults(task, defaultTarget)
	if _, exists := q.jobs[task.ID]; exists {
		return fmt.Errorf("worker job %s already exists", task.ID)
	}
	task.AfterWorkers = append([]string(nil), task.AfterWorkers...)
	q.jobs[task.ID] = &queuedJob{task: *task, projectID: task.ProjectID}
	q.pending = append(q.pending, task.ID)
	return nil
}
