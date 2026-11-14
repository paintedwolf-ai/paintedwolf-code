package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/workerlane"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerLanes runs a worker round's write job in the worker's lane: one
// branch per worker kept across rounds, refreshed after a landing and reset
// after a rejection. A reviewer's lane is a view of its sealed subject, kept
// across its rounds. A job that runs no worker round keeps its own branch.
type WorkerLanes struct {
	Lanes   *workerlane.Service
	Records *workerrecords.Store
	State   workerlane.LaneState
	// Checkpoints confirms a stacked worker's base result can still be
	// reconstructed before its first lane is prepared from it.
	Checkpoints workerlane.Checkpoints
}

// ErrStackedBaseUnavailable refuses a stacked worker whose base result
// cannot be named or reconstructed.
var ErrStackedBaseUnavailable = errors.New("stacked base result is unavailable")

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
	assignment, err := l.assignment(ctx, task, round)
	if err != nil {
		return "", true, err
	}
	facts, err := l.Lanes.FactsFor(ctx, l.State, l.Checkpoints, actor, round.WorkerID, assignment)
	if err != nil {
		return "", true, err
	}
	source := ""
	rounds, err := l.Records.Rounds(ctx, actor, round.WorkerID)
	if err != nil {
		return "", true, err
	}
	for i := len(rounds) - 1; i >= 0 && !assignment.Reviewer; i-- {
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
	if task == nil {
		return false
	}
	round, ok, err := l.round(ctx, task.ID)
	if err != nil || !ok {
		return false
	}
	_, reviewer := round.Subject()
	return task.EffectiveScope().IsWrite() || reviewer
}

// Reviews reports whether a read job reviews a sealed result, and so runs in
// a view of it rather than in current source.
func (l *WorkerLanes) Reviews(ctx context.Context, task *api.WorkerTask) bool {
	if task == nil || task.EffectiveScope().IsWrite() {
		return false
	}
	round, ok, err := l.round(ctx, task.ID)
	if err != nil || !ok {
		return false
	}
	_, reviewer := round.Subject()
	return reviewer
}

// assignment is a round's lane assignment: a reviewer reads its sealed
// subject; a stacked write task starts on its base job's settled result.
func (l *WorkerLanes) assignment(ctx context.Context, task *api.WorkerTask, round workerrecords.Round) (workerlane.Assignment, error) {
	if subject, ok := round.Subject(); ok {
		return workerlane.Assignment{Reviewer: true, SubjectResultID: subject.ResultID}, nil
	}
	a := workerlane.Assignment{Write: true}
	baseJob := strings.TrimSpace(task.EffectiveScope().BaseOverlayID)
	if baseJob == "" {
		return a, nil
	}
	names, ok, err := l.Records.NamesByJob(ctx, baseJob)
	if err != nil {
		return a, err
	}
	if !ok || names.Handle == "" {
		return a, fmt.Errorf("%w: %s has no settled result", ErrStackedBaseUnavailable, baseJob)
	}
	if l.Checkpoints == nil {
		return a, fmt.Errorf("%w: no checkpoint store", ErrStackedBaseUnavailable)
	}
	a.BaseResultID = names.Handle
	return a, nil
}
