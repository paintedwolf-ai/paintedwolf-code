// Package workerops carries out what a coordinator asks of its workers:
// instructing, shelving changes and reading history. Every operation reads
// the worker's state from workerstate, so admission agrees with what the
// roster shows.
package workerops

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workerstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// Jobs reads worker jobs.
type Jobs interface {
	Get(jobID string) (*api.WorkerTask, bool)
}

// Decisions reads a child session's pending decision.
type Decisions interface {
	Get(ctx context.Context, childSessionID string) (api.WorkerDecisionRequest, bool, error)
}

// Readiness reports a worker whose lane or latest landing needs recovery.
type Readiness func(ctx context.Context, actor workerrecords.Actor, workerID string) (recovery bool, err error)

// Reader gathers the facts a worker's state is derived from.
type Reader struct {
	Records   *workerrecords.Store
	Jobs      Jobs
	Decisions Decisions
	Readiness Readiness
	// ApprovalWait reports a tool approval pending in the child session.
	ApprovalWait func(ctx context.Context, childSessionID string) bool
}

// Snapshot is one worker with its rounds, dispositions and derived state.
type Snapshot struct {
	Worker       workerrecords.Worker
	Rounds       []workerrecords.Round
	Dispositions map[string]workerrecords.Disposition
	Facts        workerstate.Facts
	View         workerstate.View
}

// Latest returns the worker's latest round, if any.
func (s Snapshot) Latest() (workerrecords.Round, bool) {
	if len(s.Rounds) == 0 {
		return workerrecords.Round{}, false
	}
	return s.Rounds[len(s.Rounds)-1], true
}

// LatestResult returns the newest round that settled with a result.
func (s Snapshot) LatestResult() (workerrecords.Round, bool) {
	for i := len(s.Rounds) - 1; i >= 0; i-- {
		if s.Rounds[i].SettledAt != "" && s.Rounds[i].ResultID != "" {
			return s.Rounds[i], true
		}
	}
	return workerrecords.Round{}, false
}

// Read derives one worker's state.
func (r Reader) Read(ctx context.Context, actor workerrecords.Actor, w workerrecords.Worker) (Snapshot, error) {
	snap := Snapshot{Worker: w, Dispositions: map[string]workerrecords.Disposition{}}
	rounds, err := r.Records.Rounds(ctx, actor, w.ID)
	if err != nil {
		return Snapshot{}, err
	}
	snap.Rounds = rounds
	dispositions, err := r.Records.Dispositions(ctx, actor, w.ID)
	if err != nil {
		return Snapshot{}, err
	}
	for _, d := range dispositions {
		snap.Dispositions[d.ResultID] = d
	}
	f := workerstate.Facts{
		Closed:  w.ClosedAt != "",
		Planned: w.LegID != "" && len(rounds) == 0,
		Role:    workerstate.RoleUnbound,
	}
	if w.LegID != "" || w.WorkflowWorkID != "" {
		f.Role = workerstate.RolePlanned
	}
	for _, round := range rounds {
		if round.ReviewSubject != "" {
			f.Role = workerstate.RoleReviewer
		}
		if round.SettledAt == "" || round.ResultID == "" {
			continue
		}
		job, _ := r.job(round.JobID)
		d := snap.Dispositions[round.ResultID]
		if api.WorkerTaskLegStatus(job) == "complete" && d.AccountedAt == "" {
			f.UnaccountedComplete = true
		}
		if round.CheckpointID != "" {
			switch d.ChangeState {
			case "":
				f.OpenChanges = true
			case workerrecords.ChangeShelved:
				f.ShelvedChanges = true
			}
		}
	}
	if latest, ok := snap.Latest(); ok {
		f.Latest, err = r.round(ctx, w, latest)
		if err != nil {
			return Snapshot{}, err
		}
	}
	if r.Readiness != nil && !f.Closed {
		if f.Recovery, err = r.Readiness(ctx, actor, w.ID); err != nil {
			return Snapshot{}, err
		}
	}
	snap.Facts = f
	snap.View = workerstate.Derive(f)
	return snap, nil
}

// LatestResultJob names the job of the named worker's latest result. ok is
// false for a name with no worker or a worker with no result yet.
func (r Reader) LatestResultJob(ctx context.Context, actor workerrecords.Actor, name string) (string, bool, error) {
	w, err := r.Records.Resolve(ctx, actor, strings.TrimSpace(name))
	if errors.Is(err, workerrecords.ErrWorkerNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	snap, err := r.Read(ctx, actor, w)
	if err != nil {
		return "", false, err
	}
	round, ok := snap.LatestResult()
	return round.JobID, ok, nil
}

func (r Reader) job(jobID string) (api.WorkerTask, bool) {
	if r.Jobs == nil {
		return api.WorkerTask{}, false
	}
	job, ok := r.Jobs.Get(jobID)
	if !ok || job == nil {
		return api.WorkerTask{}, false
	}
	return *job, true
}

func (r Reader) round(ctx context.Context, w workerrecords.Worker, latest workerrecords.Round) (*workerstate.Round, error) {
	job, found := r.job(latest.JobID)
	out := &workerstate.Round{Settled: latest.SettledAt != ""}
	if out.Settled {
		switch api.WorkerTaskLegStatus(job) {
		case "complete", "partial", "blocked":
			out.Reported = true
		}
		return out, nil
	}
	if found {
		switch job.Status {
		case api.WorkerStatusHeld:
			out.Held = true
		case api.WorkerStatusPending:
			out.Queued = true
		case api.WorkerStatusWaiting:
			out.Parked = true
		}
	}
	child := w.ChildSessionID
	if child != "" && r.Decisions != nil {
		dec, ok, err := r.Decisions.Get(ctx, child)
		if err != nil {
			return nil, err
		}
		out.DecisionPending = ok && dec.WorkerID == latest.JobID
	}
	if child != "" && r.ApprovalWait != nil {
		out.ApprovalWait = r.ApprovalWait(ctx, child)
	}
	return out, nil
}
