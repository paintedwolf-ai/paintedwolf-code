package workerprogress_test

import (
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/workerprogress"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFromTaskSeedsLifetimeCountersOnly(t *testing.T) {
	t.Parallel()
	usage := &api.WorkerContextUsage{PromptTokens: 1234}
	seed := workerprogress.FromTask(&api.WorkerTask{
		ToolLoopsUsed: 12,
		MaxToolLoops:  40,
		ToolCallsUsed: 96,
		// A batch cannot survive the run that opened it; a resumed worker starts
		// between rounds, so these must not come back from the row.
		TurnToolCalls: 6,
		TurnToolsDone: 3,
		ContextUsage:  usage,
	})
	want := workerprogress.Snapshot{
		ToolLoopsUsed: 12,
		MaxToolLoops:  40,
		ToolCallsUsed: 96,
		ContextUsage:  usage,
	}
	if seed != want {
		t.Fatalf("seed = %+v want %+v", seed, want)
	}
	if got := workerprogress.FromTask(nil); got != (workerprogress.Snapshot{}) {
		t.Fatalf("nil task seed = %+v want zero", got)
	}
}

func TestTrackerRoundAndBatchEdges(t *testing.T) {
	t.Parallel()
	tracker := workerprogress.NewTracker(workerprogress.Snapshot{ToolLoopsUsed: 3, ToolCallsUsed: 10})

	round := tracker.CompleteRound(4, 40, &api.WorkerContextUsage{PromptTokens: 500})
	if round.ToolLoopsUsed != 4 || round.MaxToolLoops != 40 {
		t.Fatalf("round edge = %+v want used 4 max 40", round)
	}
	if round.TurnToolCalls != 0 || round.TurnToolsDone != 0 {
		t.Fatalf("round edge kept a batch: %+v", round)
	}

	open := tracker.BeginBatch(3)
	if open.TurnToolCalls != 3 || open.TurnToolsDone != 0 {
		t.Fatalf("open edge = %+v want 0 of 3", open)
	}

	first := tracker.SettleCall()
	if first.ToolCallsUsed != 11 || first.TurnToolsDone != 1 {
		t.Fatalf("settle edge = %+v want calls 11, done 1", first)
	}
	if first.ToolLoopsUsed != 4 {
		t.Fatalf("settling a call moved the round count: %+v", first)
	}

	closed := tracker.EndBatch()
	if closed.TurnToolCalls != 0 || closed.TurnToolsDone != 0 {
		t.Fatalf("close edge = %+v want no batch", closed)
	}
	if closed.ToolCallsUsed != 11 {
		t.Fatalf("close edge lost the lifetime count: %+v", closed)
	}
}

// A batch settles more calls than it opened when the host appends a synthesized
// result; the shown fraction must stay <= 1 rather than overflow.
func TestTrackerBatchGrowsToFitExtraSettledCalls(t *testing.T) {
	t.Parallel()
	tracker := workerprogress.NewTracker(workerprogress.Snapshot{})
	tracker.BeginBatch(1)
	tracker.SettleCall()
	got := tracker.SettleCall()
	if got.TurnToolsDone != 2 || got.TurnToolCalls != 2 {
		t.Fatalf("edge = %+v want 2 of 2", got)
	}
}

func TestTrackerRoundCountNeverRegresses(t *testing.T) {
	t.Parallel()
	tracker := workerprogress.NewTracker(workerprogress.Snapshot{ToolLoopsUsed: 9})
	if got := tracker.CompleteRound(4, 40, nil); got.ToolLoopsUsed != 9 {
		t.Fatalf("used = %d want the seeded 9 to hold", got.ToolLoopsUsed)
	}
}

func TestTrackerSettlesConcurrently(t *testing.T) {
	t.Parallel()
	tracker := workerprogress.NewTracker(workerprogress.Snapshot{})
	tracker.BeginBatch(64)
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tracker.SettleCall()
		}()
	}
	wg.Wait()
	if got := tracker.Current(); got.ToolCallsUsed != 64 || got.TurnToolsDone != 64 {
		t.Fatalf("edge = %+v want 64 settled", got)
	}
}

func TestNilTrackerIsInert(t *testing.T) {
	t.Parallel()
	var tracker *workerprogress.Tracker
	if got := tracker.SettleCall(); got != (workerprogress.Snapshot{}) {
		t.Fatalf("nil settle = %+v want zero", got)
	}
	if got := tracker.ToolLoopsUsed(); got != 0 {
		t.Fatalf("nil used = %d want 0", got)
	}
	tracker.SetMaxToolLoops(40)
	if got := tracker.Current(); got != (workerprogress.Snapshot{}) {
		t.Fatalf("nil current = %+v want zero", got)
	}
}

func TestApplyKeepsLifetimeCountersMonotonic(t *testing.T) {
	t.Parallel()
	task := &api.WorkerTask{ToolLoopsUsed: 12, ToolCallsUsed: 96, MaxToolLoops: 40}
	workerprogress.Apply(task, workerprogress.Snapshot{
		ToolLoopsUsed: 3,
		ToolCallsUsed: 40,
		MaxToolLoops:  0,
		TurnToolCalls: 6,
		TurnToolsDone: 2,
	})
	if task.ToolLoopsUsed != 12 || task.ToolCallsUsed != 96 {
		t.Fatalf("task = %+v want lifetime counters held", task)
	}
	if task.MaxToolLoops != 40 {
		t.Fatalf("unset ceiling overwrote the row: %d", task.MaxToolLoops)
	}
	// The batch is one moment, not an accumulation — it replaces, both ways.
	if task.TurnToolCalls != 6 || task.TurnToolsDone != 2 {
		t.Fatalf("task = %+v want batch 2 of 6", task)
	}
	workerprogress.Apply(task, workerprogress.Snapshot{ToolLoopsUsed: 13, MaxToolLoops: 60})
	if task.TurnToolCalls != 0 || task.TurnToolsDone != 0 {
		t.Fatalf("task = %+v want the batch cleared", task)
	}
	if task.ToolLoopsUsed != 13 || task.MaxToolLoops != 60 {
		t.Fatalf("task = %+v want used 13 max 60", task)
	}
}

func TestApplyKeepsContextUsageWhenEdgeCarriesNone(t *testing.T) {
	t.Parallel()
	usage := &api.WorkerContextUsage{PromptTokens: 1234}
	task := &api.WorkerTask{ContextUsage: usage}
	workerprogress.Apply(task, workerprogress.Snapshot{ToolCallsUsed: 1})
	if task.ContextUsage != usage {
		t.Fatalf("context usage = %+v want it retained", task.ContextUsage)
	}
	workerprogress.Apply(nil, workerprogress.Snapshot{})
}
