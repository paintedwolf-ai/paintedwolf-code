package worker

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/pkg/api"
)

func (q *InMemoryQueue) ListPendingOutcomes(ctx context.Context) ([]api.WorkerTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []api.WorkerTask
	for id, job := range q.jobs {
		if _, delivered := q.outcomeDelivered[id]; delivered {
			continue
		}
		switch {
		case job.task.Status.IsTerminal():
			out = append(out, q.withDependencies(job.task))
		case job.task.Status == api.WorkerStatusHeld:
			if job.task.Result != nil && strings.TrimSpace(job.task.Result.Status) == "needs_decision" {
				out = append(out, q.withDependencies(job.task))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := out[i].CreatedAt, out[j].CreatedAt
		if out[i].CompletedAt != nil {
			left = *out[i].CompletedAt
		}
		if out[j].CompletedAt != nil {
			right = *out[j].CompletedAt
		}
		if left.Equal(right) {
			return out[i].ID < out[j].ID
		}
		return left.Before(right)
	})
	if len(out) > 256 {
		out = out[:256]
	}
	return out, nil
}

func (q *InMemoryQueue) MarkOutcomeDelivered(ctx context.Context, jobID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.jobs[jobID]; !ok {
		return fmt.Errorf("job not found: %s", jobID)
	}
	q.outcomeDelivered[jobID] = struct{}{}
	return nil
}

// List returns tasks for a project, optionally filtered by status.
func (q *InMemoryQueue) List(ctx context.Context, projectID string, status ...api.WorkerStatus) ([]api.WorkerTask, error) {
	return q.list(ctx, projectID, "", "", status...)
}

// ListBySession returns tasks for a project scoped to a coordinator session.
func (q *InMemoryQueue) ListBySession(ctx context.Context, projectID, sessionID string, status ...api.WorkerStatus) ([]api.WorkerTask, error) {
	return q.list(ctx, projectID, strings.TrimSpace(sessionID), "", status...)
}

// ListByWorkflowRunID returns tasks bound to a workflow run.
func (q *InMemoryQueue) ListByWorkflowRunID(ctx context.Context, runID string, status ...api.WorkerStatus) ([]api.WorkerTask, error) {
	return q.list(ctx, "", "", strings.TrimSpace(runID), status...)
}

func (q *InMemoryQueue) ListCancellationRequestsByRunID(ctx context.Context, runID string) ([]api.WorkerTask, error) {
	tasks, err := q.ListByWorkflowRunID(ctx, runID,
		api.WorkerStatusPending, api.WorkerStatusRunning, api.WorkerStatusWaiting, api.WorkerStatusHeld, api.WorkerStatusCanceled)
	if err != nil {
		return nil, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	requested := tasks[:0]
	for _, task := range tasks {
		if job := q.jobs[task.ID]; job != nil && job.cancelRequested {
			requested = append(requested, task)
		}
	}
	return requested, nil
}

func (q *InMemoryQueue) ListByWorkspacePath(ctx context.Context, workspacePath string, status ...api.WorkerStatus) ([]api.WorkerTask, error) {
	wantKey := enginepaths.ProjectKey(workspacePath)
	if wantKey == "" {
		return nil, nil
	}
	all, err := q.list(ctx, "", "", "", status...)
	if err != nil {
		return nil, err
	}
	out := make([]api.WorkerTask, 0, len(all))
	for _, task := range all {
		if enginepaths.ProjectKey(task.WorkspacePath) == wantKey {
			out = append(out, task)
		}
	}
	return out, nil
}

// ListPendingOverlayPromote returns completed write workers awaiting overlay promote.
func (q *InMemoryQueue) ListPendingOverlayPromote(ctx context.Context, sessionID string) ([]api.WorkerTask, error) {
	tasks, err := q.ListBySession(ctx, "", sessionID, api.WorkerStatusComplete)
	if err != nil {
		return nil, err
	}
	var pending []api.WorkerTask
	for _, task := range tasks {
		if !task.EffectiveScope().IsWrite() || strings.TrimSpace(task.WorkspaceRoot) == "" {
			continue
		}
		if task.MergeStatus != api.WorkerMergeStatusPending {
			continue
		}
		pending = append(pending, task)
	}
	return pending, nil
}

// ListLiveOverlaysForSession returns actionable write overlays.
func (q *InMemoryQueue) ListLiveOverlaysForSession(ctx context.Context, sessionID string) ([]api.WorkerTask, error) {
	tasks, err := q.ListBySession(ctx, "", sessionID,
		api.WorkerStatusPending, api.WorkerStatusRunning, api.WorkerStatusWaiting, api.WorkerStatusComplete, api.WorkerStatusHeld)
	if err != nil {
		return nil, err
	}
	return filterLiveOverlays(tasks), nil
}

func (q *InMemoryQueue) list(ctx context.Context, projectID, sessionID, workflowRunID string, status ...api.WorkerStatus) ([]api.WorkerTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	filter := make(map[api.WorkerStatus]bool)
	for _, s := range status {
		filter[s] = true
	}

	var out []api.WorkerTask
	for _, job := range q.jobs {
		if projectID != "" && job.projectID != projectID && job.task.ProjectID != projectID {
			continue
		}
		if sessionID != "" && strings.TrimSpace(job.task.ParentSessionID) != sessionID {
			continue
		}
		if workflowRunID != "" && strings.TrimSpace(job.task.WorkflowRunID) != workflowRunID {
			continue
		}
		if len(filter) > 0 && !filter[job.task.Status] {
			continue
		}
		out = append(out, q.withDependencies(job.task))
	}
	return out, nil
}

// Get returns a task by ID.
func (q *InMemoryQueue) Get(jobID string) (*api.WorkerTask, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	job, ok := q.jobs[jobID]
	if !ok {
		return nil, false
	}
	t := q.withDependencies(job.task)
	return &t, true
}

// GetLatestByChildSessionID returns the newest run for a child session.
func (q *InMemoryQueue) GetLatestByChildSessionID(_ context.Context, childSessionID string) (*api.WorkerTask, bool) {
	childSessionID = strings.TrimSpace(childSessionID)
	if childSessionID == "" {
		return nil, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	var newest *api.WorkerTask
	for _, job := range q.jobs {
		if strings.TrimSpace(job.task.ChildSessionID) != childSessionID {
			continue
		}
		task := job.task
		if newest == nil || task.CreatedAt.After(newest.CreatedAt) ||
			(task.CreatedAt.Equal(newest.CreatedAt) && task.ID > newest.ID) {
			newest = &task
		}
	}
	return newest, newest != nil
}

// ListBranchJobs includes jobs whose branch is still being provisioned.
func (q *InMemoryQueue) ListBranchJobs(context.Context) ([]BranchJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []BranchJob
	for _, job := range q.jobs {
		out = append(out, BranchJob{
			ID: job.task.ID, ProjectID: job.projectID,
			Sealed: branchJobSealed(job.task.Status, job.task.MergeStatus, job.task.WorkspaceOverlayPath),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
