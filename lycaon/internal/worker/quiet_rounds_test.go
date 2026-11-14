package worker

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/pkg/api"
)

type quietHarness struct {
	now      time.Time
	jobs     []api.WorkerTask
	progress map[string]time.Time
	approval map[string]bool
	notices  []kick.WorkerRoundNoticeFacts
	parents  []string
	rounds   *QuietRounds
}

func newQuietHarness(start time.Time, after time.Duration) *quietHarness {
	h := &quietHarness{now: start, progress: map[string]time.Time{}, approval: map[string]bool{}}
	h.rounds = &QuietRounds{
		Jobs: func(context.Context) ([]api.WorkerTask, error) { return h.jobs, nil },
		Progress: func(_ context.Context, child string) (time.Time, error) {
			return h.progress[child], nil
		},
		AwaitingApproval: func(_ context.Context, child string) bool { return h.approval[child] },
		Notify: func(_ context.Context, parent string, facts kick.WorkerRoundNoticeFacts) {
			h.parents = append(h.parents, parent)
			h.notices = append(h.notices, facts)
		},
		After: after,
		Now:   func() time.Time { return h.now },
	}
	return h
}

// advance moves the clock and runs one maintenance pass.
func (h *quietHarness) advance(t *testing.T, d time.Duration) {
	t.Helper()
	h.now = h.now.Add(d)
	h.rounds.Check(t.Context())
}

func runningJob(id, child string, started time.Time) api.WorkerTask {
	return api.WorkerTask{
		ID: id, ParentSessionID: "parent-" + id, ChildSessionID: child, AgentType: "implementer",
		Status: api.WorkerStatusRunning, StartedAt: &started, CreatedAt: started, ToolLoopsUsed: 7,
	}
}

func TestQuietRoundsNotifyAtThresholdAndEachDoubling(t *testing.T) {
	start := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	after := 10 * time.Minute
	h := newQuietHarness(start, after)
	h.jobs = []api.WorkerTask{runningJob("job-1", "child-1", start)}
	h.rounds.Check(t.Context())

	for _, step := range []struct {
		advance time.Duration
		want    int
	}{
		{after - time.Minute, 0},
		{time.Minute, 1},
		{after / 2, 1},
		{after / 2, 2},
		{after, 2},
		{after, 3},
	} {
		h.advance(t, step.advance)
		if len(h.notices) != step.want {
			t.Fatalf("at %s notices = %d, want %d", h.now.Sub(start), len(h.notices), step.want)
		}
	}
	first := h.notices[0]
	if first.JobID != "job-1" || first.Kind != kick.RoundNoticeQuiet || first.InFlight != kick.RoundInFlightModelCall ||
		first.ModelCalls != 7 || first.QuietFor != after || h.parents[0] != "parent-job-1" {
		t.Fatalf("first notice = %+v to %q", first, h.parents[0])
	}
}

func TestQuietRoundsCountFromLatestProgress(t *testing.T) {
	start := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	after := 10 * time.Minute
	h := newQuietHarness(start, after)
	h.jobs = []api.WorkerTask{runningJob("job-1", "child-1", start)}
	h.rounds.Check(t.Context())

	h.advance(t, 8*time.Minute)
	h.progress["child-1"] = h.now
	h.advance(t, 8*time.Minute)
	if len(h.notices) != 0 {
		t.Fatalf("progress did not reset quiet time: %+v", h.notices)
	}
	h.advance(t, 2*time.Minute)
	if len(h.notices) != 1 || h.notices[0].QuietFor != after {
		t.Fatalf("notices = %+v, want one at %s after the progress", h.notices, after)
	}
}

// Time before this host started does not count, so a restart does not raise
// notices for rounds that were quiet only while the host was down.
func TestQuietRoundsCountFromHostStart(t *testing.T) {
	hostStart := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	after := 10 * time.Minute
	h := newQuietHarness(hostStart, after)
	h.jobs = []api.WorkerTask{runningJob("job-1", "child-1", hostStart.Add(-2*time.Hour))}
	h.rounds.Check(t.Context())

	h.advance(t, after-time.Second)
	if len(h.notices) != 0 {
		t.Fatalf("downtime counted as quiet: %+v", h.notices)
	}
	h.advance(t, time.Second)
	if len(h.notices) != 1 {
		t.Fatalf("notices = %d, want 1 once quiet since host start", len(h.notices))
	}
}

func TestQuietRoundsSkipRoundsWaitingOnPurpose(t *testing.T) {
	start := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	h := newQuietHarness(start, time.Minute)
	approval := runningJob("job-approval", "child-approval", start)
	h.approval["child-approval"] = true
	held := runningJob("job-held", "child-held", start)
	held.Status = api.WorkerStatusHeld
	waiting := runningJob("job-waiting", "child-waiting", start)
	waiting.Status = api.WorkerStatusWaiting
	orphan := runningJob("job-orphan", "child-orphan", start)
	orphan.ParentSessionID = " "
	h.jobs = []api.WorkerTask{approval, held, waiting, orphan}
	h.rounds.Check(t.Context())
	h.advance(t, time.Hour)
	if len(h.notices) != 0 {
		t.Fatalf("waiting rounds raised notices: %+v", h.notices)
	}
}

func TestQuietRoundsReportWhatIsInFlight(t *testing.T) {
	start := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	h := newQuietHarness(start, time.Minute)
	queued := runningJob("job-queued", "", start)
	queued.Status = api.WorkerStatusPending
	tools := runningJob("job-tools", "child-tools", start)
	tools.TurnToolCalls, tools.TurnToolsDone = 5, 2
	h.jobs = []api.WorkerTask{queued, tools}
	h.rounds.Check(t.Context())
	h.advance(t, time.Minute)
	if len(h.notices) != 2 {
		t.Fatalf("notices = %+v, want one per round", h.notices)
	}
	if got := h.notices[0]; got.JobID != "job-queued" || got.InFlight != kick.RoundInFlightQueued {
		t.Fatalf("queued notice = %+v", got)
	}
	if got := h.notices[1]; got.JobID != "job-tools" || got.InFlight != kick.RoundInFlightTools || got.ToolCalls != 5 || got.ToolsDone != 2 {
		t.Fatalf("tool batch notice = %+v", got)
	}
}
