package worker

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

// recoverPrerequisiteJobs settles consumers that cannot obtain successful input.
func recoverPrerequisiteJobs(ctx context.Context, queue WorkerQueue, blocked func(context.Context) ([]string, error)) ([]api.WorkerTask, error) {
	var canceled []api.WorkerTask
	for {
		ids, err := blocked(ctx)
		if err != nil {
			return canceled, err
		}
		if len(ids) == 0 {
			return canceled, nil
		}
		for _, id := range ids {
			result := api.WorkerResult{Status: "canceled", Summary: "A prerequisite worker cannot supply successful output.", HostAssembled: true}
			if err := queue.Cancel(ctx, id, &result); err != nil {
				return canceled, err
			}
			if task, ok := queue.Get(id); ok {
				canceled = append(canceled, *task)
			}
		}
	}
}

func (q *SQLQueue) recoverPrerequisites(ctx context.Context) ([]api.WorkerTask, error) {
	return recoverPrerequisiteJobs(ctx, q, q.store.queries.ListBlockedWorkerPrerequisites)
}

func (q *InMemoryQueue) recoverPrerequisites(ctx context.Context) ([]api.WorkerTask, error) {
	return recoverPrerequisiteJobs(ctx, q, q.blockedPrerequisites)
}

func (q *InMemoryQueue) blockedPrerequisites(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	var ids []string
	for id, job := range q.jobs {
		if job.task.Status != api.WorkerStatusPending {
			continue
		}
		for _, dependency := range q.withDependencies(job.task).Dependencies {
			if dependency.State == "blocked" {
				ids = append(ids, id)
				break
			}
		}
	}
	return ids, nil
}
