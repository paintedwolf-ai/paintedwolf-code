// Package workerops carries out what a coordinator asks of its workers:
// instructing, shelving changes and reading history. Every operation reads
// the worker's state from workerstate, so admission agrees with what the
// roster shows.
package workerops

import (
	"context"
	"errors"
	"slices"
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
	Worker               workerrecords.Worker
	Rounds               []workerrecords.Round
	Dispositions         map[string]workerrecords.Disposition
	Facts                workerstate.Facts
	View                 workerstate.View
	LatestResultRound    *workerrecords.Round
	LatestChangedSettled *workerrecords.Round
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
	if s.LatestResultRound != nil {
		return *s.LatestResultRound, true
	}
	for i := len(s.Rounds) - 1; i >= 0; i-- {
		if s.Rounds[i].SettledAt != "" && s.Rounds[i].ResultID != "" {
			return s.Rounds[i], true
		}
	}
	return workerrecords.Round{}, false
}

// LatestChanged returns the newest settled round that recorded file changes.
func (s Snapshot) LatestChanged() (workerrecords.Round, bool) {
	if s.LatestChangedSettled != nil {
		return *s.LatestChangedSettled, true
	}
	for i := len(s.Rounds) - 1; i >= 0; i-- {
		if s.Rounds[i].SettledAt != "" && s.Rounds[i].CheckpointID != "" && s.Rounds[i].ResultID != "" {
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
	// A worker that has run but lost its child session cannot work again.
	if !f.Closed && w.ChildSessionID == "" {
		for _, round := range rounds {
			f.SessionUnusable = f.SessionUnusable || round.SettledAt != ""
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

// ReadBounded derives one worker's state and loads at most recentLimit newest
// rounds (zero loads only the latest round when present), keeping query work
// independent of total round history.
func (r Reader) ReadBounded(ctx context.Context, actor workerrecords.Actor, w workerrecords.Worker, recentLimit int) (Snapshot, error) {
	summary, err := r.Records.ReadStateSummary(ctx, actor, w.ID)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{
		Worker:               w,
		Dispositions:         map[string]workerrecords.Disposition{},
		LatestResultRound:    summary.LatestResult,
		LatestChangedSettled: summary.LatestChangedSettled,
	}
	if summary.LatestResult != nil {
		snap.Dispositions[summary.LatestResult.ResultID] = summary.LatestResultDisp
	}
	if summary.LatestChangedSettled != nil && summary.LatestChangedSettled.ResultID != "" {
		if _, exists := snap.Dispositions[summary.LatestChangedSettled.ResultID]; !exists {
			snap.Dispositions[summary.LatestChangedSettled.ResultID] = summary.LatestChangedDisp
		}
	}
	if recentLimit > 0 && summary.HasRounds {
		recent, rErr := r.Records.RoundsBefore(ctx, actor, w.ID, 0, recentLimit)
		if rErr != nil {
			return Snapshot{}, rErr
		}
		slices.Reverse(recent)
		snap.Rounds = recent
	} else if summary.LatestRound != nil {
		snap.Rounds = []workerrecords.Round{*summary.LatestRound}
	}
	f := workerstate.Facts{
		Closed:         w.ClosedAt != "",
		Planned:        w.LegID != "" && !summary.HasRounds,
		Role:           workerstate.RoleUnbound,
		OpenChanges:    summary.HasOpenChanges,
		ShelvedChanges: summary.HasShelvedChanges,
	}
	if w.LegID != "" || w.WorkflowWorkID != "" {
		f.Role = workerstate.RolePlanned
	}
	if summary.HasReviewerRound {
		f.Role = workerstate.RoleReviewer
	}
	for _, jobID := range summary.UnaccountedJobs {
		job, _ := r.job(jobID)
		if api.WorkerTaskLegStatus(job) == "complete" {
			f.UnaccountedComplete = true
			break
		}
	}
	if !f.Closed && w.ChildSessionID == "" && summary.HasSettledRound {
		f.SessionUnusable = true
	}
	if summary.LatestRound != nil {
		f.Latest, err = r.round(ctx, w, *summary.LatestRound)
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
	snap, err := r.ReadBounded(ctx, actor, w, 0)
	if err != nil {
		return "", false, err
	}
	round, ok := snap.LatestResult()
	return round.JobID, ok, nil
}

// CountState counts the actor's session's workers in state.
func (r Reader) CountState(ctx context.Context, actor workerrecords.Actor, state workerstate.State) (int, error) {
	workers, err := r.Records.Workers(ctx, actor)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, w := range workers {
		snap, err := r.ReadBounded(ctx, actor, w, 0)
		if err != nil {
			return 0, err
		}
		if snap.View.State == state {
			n++
		}
	}
	return n, nil
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

// PrerequisitePending reports a job one of whose producers has no ready
// output yet. A long-lived producer is judged on its latest round, which
// latestRoundJob names for the job a prerequisite was given as.
func PrerequisitePending(ctx context.Context, latestRoundJob func(context.Context, string) (string, error), jobs Jobs, job api.WorkerTask) (bool, error) {
	for _, id := range job.AfterWorkers {
		latest, err := latestRoundJob(ctx, id)
		if err != nil {
			return false, err
		}
		upstream, _ := jobs.Get(latest)
		if !api.WorkerOutputReady(upstream) {
			return true, nil
		}
	}
	return false, nil
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
	child := w.ChildSessionID
	if child != "" && r.Decisions != nil {
		dec, ok, err := r.Decisions.Get(ctx, child)
		if err != nil {
			return nil, err
		}
		out.DecisionPending = ok && dec.WorkerID == latest.JobID
	}
	if found {
		switch job.Status {
		case api.WorkerStatusHeld:
			// A job suspended on its decision is held too; it waits on the
			// coordinator's answer, not on a prerequisite.
			out.Held = !out.DecisionPending
		case api.WorkerStatusPending:
			// A job waits on its prerequisites before it queues for a slot.
			waiting, err := PrerequisitePending(ctx, r.Records.LatestRoundJob, r.Jobs, job)
			if err != nil {
				return nil, err
			}
			out.Held, out.Queued = waiting, !waiting
		case api.WorkerStatusWaiting:
			out.Parked = true
		default:
		}
	}
	if child != "" && r.ApprovalWait != nil {
		out.ApprovalWait = r.ApprovalWait(ctx, child)
	}
	return out, nil
}
