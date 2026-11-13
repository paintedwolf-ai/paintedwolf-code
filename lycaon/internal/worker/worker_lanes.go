package worker

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/workerlane"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerLanes runs a worker round's write job in the worker's lane: one
// branch per worker kept across rounds, refreshed after a landing and reset
// after a rejection. A job that runs no worker round keeps its own branch.
type WorkerLanes struct {
	Lanes   *workerlane.Service
	Records *workerrecords.Store
	State   workerlane.LaneState
}

func (l *WorkerLanes) round(ctx context.Context, jobID string) (workerrecords.Round, bool, error) {
	if l == nil || l.Records == nil || l.Lanes == nil {
		return workerrecords.Round{}, false, nil
	}
	return l.Records.RoundByJob(ctx, jobID)
}

// Prepare readies the lane a round's job runs in. ok is false for a job that
// runs no worker round.
func (l *WorkerLanes) Prepare(ctx context.Context, task *api.WorkerTask, roots []projectroot.RootRef) (string, bool, error) {
	round, ok, err := l.round(ctx, task.ID)
	if err != nil || !ok {
		return "", false, err
	}
	actor := workerrecords.Actor{SessionID: task.ParentSessionID, Role: workerrecords.RoleCoordinator, PrincipalID: task.ParentSessionID}
	facts, err := l.Lanes.FactsFor(ctx, l.State, nil, actor, round.WorkerID, workerlane.Assignment{Write: true})
	if err != nil {
		return "", true, err
	}
	source := ""
	rounds, err := l.Records.Rounds(ctx, actor, round.WorkerID)
	if err != nil {
		return "", true, err
	}
	for i := len(rounds) - 1; i >= 0; i-- {
		if rounds[i].SettledAt != "" && rounds[i].ResultID != "" {
			source = rounds[i].ResultID
			break
		}
	}
	binding, err := l.Lanes.Admit(ctx, workerlane.Request{
		Actor: actor, Op: workerrecords.Op{ID: "lane:" + task.ID}, WorkerID: round.WorkerID, Facts: facts,
		Roots: roots, ActiveRootID: strings.TrimSpace(task.WorkspaceRootID), SourceResultID: source,
	})
	if err != nil {
		return "", true, err
	}
	return binding.Root, true, nil
}

// Holds reports whether a job's workspace is its worker's lane, which
// outlives the job: completion, failure and cancellation leave it in place.
func (l *WorkerLanes) Holds(ctx context.Context, task *api.WorkerTask) bool {
	if task == nil || !task.EffectiveScope().IsWrite() {
		return false
	}
	_, ok, err := l.round(ctx, task.ID)
	return err == nil && ok
}
