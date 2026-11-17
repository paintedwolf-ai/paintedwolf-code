package app

import (
	"context"

	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

type waitWorkerRecords interface {
	Resolve(ctx context.Context, actor workerrecords.Actor, name string) (workerrecords.Worker, error)
	RoundByJob(ctx context.Context, jobID string) (workerrecords.Round, bool, error)
}

type waitJobs interface {
	Lookup(ctx context.Context, id string) (*api.WorkerTask, bool)
}

// waitWorkerKnown reports whether a wait handle names a worker of the session, or a job of it that runs
// no worker round. A worker's settlement is matched by its name, so its raw job id is not a handle.
func waitWorkerKnown(records waitWorkerRecords, jobs waitJobs) func(ctx context.Context, sessionID, handle string) bool {
	return func(ctx context.Context, sessionID, handle string) bool {
		actor := workerrecords.Actor{SessionID: sessionID, Role: workerrecords.RoleCoordinator, PrincipalID: sessionID}
		if _, err := records.Resolve(ctx, actor, handle); err == nil {
			return true
		}
		if _, hasRound, err := records.RoundByJob(ctx, handle); err != nil || hasRound {
			return false
		}
		task, ok := jobs.Lookup(ctx, handle)
		return ok && task.ParentSessionID == sessionID
	}
}
