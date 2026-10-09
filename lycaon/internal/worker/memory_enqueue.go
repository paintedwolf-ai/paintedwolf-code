package worker

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/worker/jobstate"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"time"
)

// refreshBoard runs outside q.mu because board reads can re-enter the queue.
func (q *InMemoryQueue) refreshBoard(ctx context.Context, task api.WorkerTask, projectID string) {
	if q.events == nil {
		return
	}
	key := strings.TrimSpace(task.ProjectID)
	if key == "" {
		key = strings.TrimSpace(projectID)
	}
	if q.events.Hub != nil {
		publishKey := events.PublishKeyFor(ctx, q.events.Lookup, key, strings.TrimSpace(task.ParentSessionID))
		publishKey.Facet = task.ID
		_ = q.events.Hub.Publish(ctx, api.EventTopicWorker, publishKey, jobstate.JobEvent(task))
	}
	q.events.PublishBoard(ctx, key, strings.TrimSpace(task.ParentSessionID))
}

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
func (q *InMemoryQueue) Enqueue(ctx context.Context, task api.WorkerTask) (string, error) {
	return q.EnqueueWithProjectID(ctx, "", task)
}

// EnqueueWithProjectID enqueues a task scoped to a project id.
func (q *InMemoryQueue) EnqueueWithProjectID(ctx context.Context, projectID string, task api.WorkerTask) (string, error) {
	var jobID string
	err := q.WithTaskAdmission(ctx, task, func() error {
		var enqueueErr error
		jobID, enqueueErr = q.enqueueAdmitted(ctx, projectID, task)
		return enqueueErr
	})
	return jobID, err
}

func (q *InMemoryQueue) enqueueAdmitted(ctx context.Context, projectID string, task api.WorkerTask) (string, error) {
	if err := q.PrepareEnqueue(ctx, projectID, &task); err != nil {
		return "", err
	}
	q.PublishEnqueued(ctx, task.ID)
	return task.ID, nil
}

func (q *InMemoryQueue) PrepareEnqueue(ctx context.Context, projectID string, task *api.WorkerTask) error {
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
	if task.WorkflowRunID != "" {
		// Workflow tasks require a runnable-state source.
		if workflowRuns == nil || workflowRuns.Runs == nil {
			return fmt.Errorf("worker queue: workflow run checker required for workflow-bound task %s", task.WorkflowRunID)
		}
		if err := workflowRuns.Runs.AssertRunnable(ctx, task.WorkflowRunID); err != nil {
			return err
		}
		if task.WorkflowPhase != "" {
			if workflowRuns.Tasks == nil {
				return fmt.Errorf("worker queue: workflow task admission required for phase-bound task %s", task.WorkflowPhase)
			}
			if err := workflowRuns.Tasks.AssertWorkerTask(ctx, task); err != nil {
				return err
			}
		}
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
	if task.EffectiveScope().IsWrite() && strings.TrimSpace(task.OverlayID) == "" {
		task.OverlayID = task.ID
	}
	if task.Status == "" {
		task.Status = api.WorkerStatusPending
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now().UTC()
	}
	if task.ExecutionTarget == "" {
		task.ExecutionTarget = defaultTarget
	}

	if _, exists := q.jobs[task.ID]; exists {
		return fmt.Errorf("worker job %s already exists", task.ID)
	}
	task.AfterWorkers = append([]string(nil), task.AfterWorkers...)
	q.jobs[task.ID] = &queuedJob{task: *task, projectID: task.ProjectID}
	q.pending = append(q.pending, task.ID)
	return nil
}

func (q *InMemoryQueue) PublishEnqueued(ctx context.Context, jobID string) {
	q.mu.Lock()
	job, ok := q.jobs[jobID]
	_, published := q.published[jobID]
	if ok && !published {
		q.published[jobID] = struct{}{}
	}
	q.mu.Unlock()
	if ok && !published && job != nil {
		q.refreshBoard(ctx, job.task, job.projectID)
	}
	q.NotifyRunnable()
}
