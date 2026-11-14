package worker

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

// QuietRounds tells a coordinator when one of its working rounds has made no
// durable progress for a while. It informs; it never stops or slows a round.
type QuietRounds struct {
	// Jobs lists pending and running worker jobs.
	Jobs func(ctx context.Context) ([]api.WorkerTask, error)
	// Progress is the time of a child session's latest durable turn progress.
	Progress func(ctx context.Context, childSessionID string) (time.Time, error)
	// AwaitingApproval reports a child session held at a person's approval.
	AwaitingApproval func(ctx context.Context, childSessionID string) bool
	// Notify delivers a round notice to the worker's coordinator.
	Notify func(ctx context.Context, parentID string, facts kick.WorkerRoundNoticeFacts)
	After  time.Duration
	Now    func() time.Time

	started  time.Time
	lastPass time.Time
}

// Check raises a notice for each working round whose quiet time crossed the
// threshold or one of its doublings since the previous check.
func (q *QuietRounds) Check(ctx context.Context) {
	if q == nil || q.Jobs == nil || q.Notify == nil {
		return
	}
	now := q.now()
	if q.started.IsZero() {
		q.started, q.lastPass = now, now
		return
	}
	prev := q.lastPass
	q.lastPass = now
	after := q.After
	if after <= 0 {
		after = spawn.DefaultRoundPolicy().QuietNoticeAfter
	}
	jobs, err := q.Jobs(ctx)
	if err != nil {
		workerPollerLog.Warn("list worker rounds for quiet notices", "error", err)
		return
	}
	for i := range jobs {
		task := &jobs[i]
		if !q.working(ctx, task) {
			continue
		}
		since := q.quietSince(ctx, task)
		if spawn.ThresholdCrossed(int64(prev.Sub(since)), int64(now.Sub(since)), int64(after)) {
			q.Notify(ctx, task.ParentSessionID, quietFacts(task, now.Sub(since)))
		}
	}
}

// working excludes rounds that are parked, asking or held at an approval:
// they are waiting on purpose, not quiet.
func (q *QuietRounds) working(ctx context.Context, task *api.WorkerTask) bool {
	if strings.TrimSpace(task.ParentSessionID) == "" {
		return false
	}
	switch task.Status {
	case api.WorkerStatusPending:
		return true
	case api.WorkerStatusRunning:
		child := strings.TrimSpace(task.ChildSessionID)
		return child == "" || q.AwaitingApproval == nil || !q.AwaitingApproval(ctx, child)
	default:
		return false
	}
}

// quietSince is the later of the round's latest durable progress, its start
// and this host's start, so time the host was down does not count.
func (q *QuietRounds) quietSince(ctx context.Context, task *api.WorkerTask) time.Time {
	since := q.started
	if started := roundStartTime(task); started.After(since) {
		since = started
	}
	if child := strings.TrimSpace(task.ChildSessionID); child != "" && q.Progress != nil {
		if progressed, err := q.Progress(ctx, child); err == nil && progressed.After(since) {
			since = progressed
		}
	}
	return since
}

func (q *QuietRounds) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now()
}

func roundStartTime(task *api.WorkerTask) time.Time {
	if task.StartedAt != nil {
		return *task.StartedAt
	}
	return task.CreatedAt
}

func quietFacts(task *api.WorkerTask, quiet time.Duration) kick.WorkerRoundNoticeFacts {
	facts := kick.WorkerRoundNoticeFacts{
		JobID: task.ID, AgentType: task.AgentType, Kind: kick.RoundNoticeQuiet,
		ModelCalls: task.ToolLoopsUsed, QuietFor: quiet,
	}
	switch {
	case task.Status == api.WorkerStatusPending:
		facts.InFlight = kick.RoundInFlightQueued
	case task.TurnToolCalls > task.TurnToolsDone:
		facts.InFlight = kick.RoundInFlightTools
		facts.ToolCalls, facts.ToolsDone = task.TurnToolCalls, task.TurnToolsDone
	default:
		facts.InFlight = kick.RoundInFlightModelCall
	}
	return facts
}
