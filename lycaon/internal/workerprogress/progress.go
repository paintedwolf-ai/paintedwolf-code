// Package workerprogress carries a worker's live progress between the prompt
// loop that observes it, the queue that publishes it, and the job row that
// checkpoints it.
package workerprogress

import (
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

// Snapshot records a worker's tool budgets, in-flight batch state, and context usage.
type Snapshot struct {
	ToolLoopsUsed int
	MaxToolLoops  int
	ToolCallsUsed int
	TurnToolCalls int
	TurnToolsDone int
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
		MaxToolLoops:  max(task.MaxToolLoops, 0),
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
	seed.TurnToolCalls = 0
	seed.TurnToolsDone = 0
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

// ToolLoopsUsed reports lifetime rounds — the count the loop bounds against.
func (t *Tracker) ToolLoopsUsed() int {
	return t.Current().ToolLoopsUsed
}

// SetMaxToolLoops records a re-synced budget ceiling (host floor, extend_worker_budget).
// It carries the ceiling for a run that ends before completing a round.
func (t *Tracker) SetMaxToolLoops(ceiling int) {
	if t == nil || ceiling <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.snap.MaxToolLoops = ceiling
}

// CompleteRound records a finished model turn: the round is spent, its context
// occupancy is current, and no batch is running yet.
func (t *Tracker) CompleteRound(used, ceiling int, usage *api.WorkerContextUsage) Snapshot {
	if t == nil {
		return Snapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if used > t.snap.ToolLoopsUsed {
		t.snap.ToolLoopsUsed = used
	}
	if ceiling > 0 {
		t.snap.MaxToolLoops = ceiling
	}
	if usage != nil {
		t.snap.ContextUsage = usage
	}
	t.snap.TurnToolCalls = 0
	t.snap.TurnToolsDone = 0
	return t.snap
}

// BeginBatch opens the tool batch this round will execute.
func (t *Tracker) BeginBatch(calls int) Snapshot {
	if t == nil {
		return Snapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.snap.TurnToolCalls = max(calls, 0)
	t.snap.TurnToolsDone = 0
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
	t.snap.TurnToolCalls = 0
	t.snap.TurnToolsDone = 0
	return t.snap
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
	if snap.MaxToolLoops > 0 {
		task.MaxToolLoops = snap.MaxToolLoops
	}
	task.TurnToolCalls = snap.TurnToolCalls
	task.TurnToolsDone = snap.TurnToolsDone
	if snap.ContextUsage != nil {
		task.ContextUsage = snap.ContextUsage
	}
}
