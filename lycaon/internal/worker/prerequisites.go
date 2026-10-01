package worker

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/pkg/api"
)

const MaxWorkerPrerequisites = 32

var ErrWorkerPrerequisitesPending = errors.New("worker prerequisites not ready")

func parseAfterWorkers(args map[string]any) ([]string, error) {
	raw, exists := args["after_workers"]
	if !exists {
		return nil, nil
	}
	var ids []string
	switch values := raw.(type) {
	case []any:
		for _, v := range values {
			s, ok := v.(string)
			if !ok {
				return nil, invalidTaskCharter("after_workers", "expected_job_ids")
			}
			ids = append(ids, s)
		}
	case []string:
		ids = append(ids, values...)
	default:
		return nil, invalidTaskCharter("after_workers", "expected_job_ids")
	}
	if len(ids) > MaxWorkerPrerequisites {
		return nil, invalidTaskCharter("after_workers", "too_many_prerequisites")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil || seen[id] {
			return nil, invalidTaskCharter("after_workers", "invalid_or_repeated_job_id")
		}
		seen[id] = true
	}
	return ids, nil
}

func validatePrerequisites(ctx context.Context, task api.WorkerTask, get func(string) (*api.WorkerTask, bool)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(task.AfterWorkers) == 0 {
		return nil
	}
	if len(task.AfterWorkers) > MaxWorkerPrerequisites || task.ChildSessionID != "" || task.EffectiveScope().BaseOverlayID != "" {
		return invalidTaskCharter("after_workers", "requires_fresh_primary_workspace")
	}
	seen := make(map[string]bool, len(task.AfterWorkers))
	for _, id := range task.AfterWorkers {
		upstream, ok := get(id)
		if !ok || upstream == nil || id == task.ID || seen[id] || upstream.ParentSessionID != task.ParentSessionID || upstream.ProjectID != task.ProjectID || upstream.WorkspaceRootID != task.WorkspaceRootID || upstream.PrimaryRootPath() != task.PrimaryRootPath() {
			return invalidTaskCharter("after_workers", "requires_distinct_existing_sibling_jobs_in_same_root")
		}
		seen[id] = true
	}
	return nil
}

// prerequisitesReady runs under the queue lock; SQL claims use the same predicate.
func (q *InMemoryQueue) prerequisitesReady(task api.WorkerTask) bool {
	for _, id := range task.AfterWorkers {
		job := q.jobs[id]
		if job == nil || !api.WorkerOutputReady(&job.task) {
			return false
		}
	}
	return true
}

func (q *InMemoryQueue) withDependencies(task api.WorkerTask) api.WorkerTask {
	task.AfterWorkers = append([]string(nil), task.AfterWorkers...)
	task.Dependencies = nil
	for _, id := range task.AfterWorkers {
		var upstream *api.WorkerTask
		if job := q.jobs[id]; job != nil {
			upstream = &job.task
		}
		task.Dependencies = append(task.Dependencies, api.WorkerDependency{WorkerID: id, State: api.WorkerOutputState(upstream)})
	}
	return task
}
