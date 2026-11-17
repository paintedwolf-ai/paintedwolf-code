// Package workerprogress carries a worker's live progress between the prompt
// loop that observes it, the queue that publishes it, and the job row that
// checkpoints it.
package workerprogress

import (
	"slices"
	"sync"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// Snapshot records a worker's usage counters, in-flight batch state, and context usage.
type Snapshot struct {
	ToolLoopsUsed int
	ToolCallsUsed int
	TurnToolCalls int
	TurnToolsDone int
	// TurnTools and TurnStartedAt name the in-flight batch and when it began.
	TurnTools     []string
	TurnStartedAt time.Time
	ContextUsage  *api.WorkerContextUsage
}

// FromTask seeds a run from the persisted job row. The in-flight batch is not
// seeded: a resumed run starts between rounds by definition.
func FromTask(task *api.WorkerTask) Snapshot {
	if task == nil {
		return Snapshot{}
	}
	return Snapshot{
		ToolLoopsUsed: max(task.ToolLoopsUsed, 0),
		ToolCallsUsed: max(task.ToolCallsUsed, 0),
		ContextUsage:  task.ContextUsage,
	}
}

// Tracker accumulates one prompt-loop run's progress. Its mutators return the
// edge to publish, so an edge is never observed half-applied. Tool batches run
// concurrently, so every read and write takes the lock.
type Tracker struct {
	mu   sync.Mutex
	snap Snapshot
}

// NewTracker starts from a seed — the persisted row on resume, zero on a first run.
func NewTracker(seed Snapshot) *Tracker {
	seed.clearBatch()
	return &Tracker{snap: seed}
}

// Current returns the latest edge.
func (t *Tracker) Current() Snapshot {
	if t == nil {
		return Snapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snap
}

// ToolLoopsUsed reports the model turns this job has completed.
func (t *Tracker) ToolLoopsUsed() int {
	return t.Current().ToolLoopsUsed
}

// CompleteRound records a finished model turn: the round is spent, its context
// occupancy is current, and no batch is running yet.
func (t *Tracker) CompleteRound(used int, usage *api.WorkerContextUsage) Snapshot {
	if t == nil {
		return Snapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if used > t.snap.ToolLoopsUsed {
		t.snap.ToolLoopsUsed = used
	}
	if usage != nil {
		t.snap.ContextUsage = usage
	}
	t.snap.clearBatch()
	return t.snap
}

// BeginBatch opens the tool batch this round will execute: one call per name,
// started at startedAt.
func (t *Tracker) BeginBatch(names []string, startedAt time.Time) Snapshot {
	if t == nil {
		return Snapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.snap.TurnToolCalls = len(names)
	t.snap.TurnToolsDone = 0
	t.snap.TurnTools = nil
	for _, name := range names {
		if !slices.Contains(t.snap.TurnTools, name) {
			t.snap.TurnTools = append(t.snap.TurnTools, name)
		}
	}
	t.snap.TurnStartedAt = startedAt
	return t.snap
}

// SettleCall records one tool call finishing, whatever its outcome — a rejected
// or errored call spent the same wall-clock the card is meant to reflect.
func (t *Tracker) SettleCall() Snapshot {
	if t == nil {
		return Snapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.snap.ToolCallsUsed++
	t.snap.TurnToolsDone++
	// A batch can settle more calls than it opened when the host appends a
	// synthesized result; the shown fraction stays truthful by growing with it.
	if t.snap.TurnToolsDone > t.snap.TurnToolCalls {
		t.snap.TurnToolCalls = t.snap.TurnToolsDone
	}
	return t.snap
}

// EndBatch closes the in-flight batch without spending a round.
func (t *Tracker) EndBatch() Snapshot {
	if t == nil {
		return Snapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.snap.clearBatch()
	return t.snap
}

func (s *Snapshot) clearBatch() {
	s.TurnToolCalls, s.TurnToolsDone = 0, 0
	s.TurnTools, s.TurnStartedAt = nil, time.Time{}
}

// Apply merges an edge without decreasing lifetime counters.
// The in-flight batch is a snapshot and is replaced whole.
func Apply(task *api.WorkerTask, snap Snapshot) {
	if task == nil {
		return
	}
	if snap.ToolLoopsUsed > task.ToolLoopsUsed {
		task.ToolLoopsUsed = snap.ToolLoopsUsed
	}
	if snap.ToolCallsUsed > task.ToolCallsUsed {
		task.ToolCallsUsed = snap.ToolCallsUsed
	}
	task.TurnToolCalls = snap.TurnToolCalls
	task.TurnToolsDone = snap.TurnToolsDone
	task.TurnTools = slices.Clone(snap.TurnTools)
	task.TurnStartedAt = nil
	if !snap.TurnStartedAt.IsZero() {
		at := snap.TurnStartedAt
		task.TurnStartedAt = &at
	}
	if snap.ContextUsage != nil {
		task.ContextUsage = snap.ContextUsage
	}
}
