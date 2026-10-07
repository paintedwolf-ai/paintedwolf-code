package spawn

import (
	"time"

	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
)

// Host caps are surfaced in prompts and enforced at runtime.
const (
	MaxConcurrentToolCalls    = 64
	DefaultWorkerMaxToolLoops = 20
	// Last iteration is prose-only, so a budget below 2 cannot finish.
	WorkerToolBudgetFloor      = 2
	DefaultWorkerToolBudgetMax = 120
)

// WorkerToolBudget bounds task max_tool_loops for every worker.
type WorkerToolBudget struct {
	Default int
	Min     int
	Max     int
}

// DefaultWorkerToolBudget returns the built-in budget bounds.
func DefaultWorkerToolBudget() WorkerToolBudget {
	return WorkerToolBudget{
		Default: DefaultWorkerMaxToolLoops,
		Min:     WorkerToolBudgetFloor,
		Max:     DefaultWorkerToolBudgetMax,
	}
}

// Effective selects an explicit ceiling or the default.
func (b WorkerToolBudget) Effective(requested int) int {
	if requested > 0 {
		return requested
	}
	return b.Default
}

// Clamp bounds a requested ceiling to the host range.
func (b WorkerToolBudget) Clamp(max int) int {
	return clampInt(max, b.Min, b.Max)
}

// Runway bounds, in tool rounds, for the low-runway notice.
const (
	workerRunwayPercent = 33
	workerRunwayMin     = 3
	workerRunwayMax     = 10
)

// WorkerRepairRounds is how many tool rounds a citation grounding retry adds
// past a worker's ceiling, so a repair never lands on a spent budget.
const WorkerRepairRounds = 3

// WorkerBudgetAnswerWait bounds how long a worker whose budget request is
// still open waits before its final round for the coordinator's answer. It
// covers one coordinator turn, including a wake deferred behind a busy one.
const WorkerBudgetAnswerWait = 2 * time.Minute

// WorkerRunway is how many rounds before its ceiling a worker is told its
// runway is low: a third of the ceiling, between 3 and 10 rounds, and
// always short of the ceiling itself so the notice precedes the first round.
func WorkerRunway(max int) int {
	if max <= 1 {
		return 0
	}
	runway := clampInt((max*workerRunwayPercent+99)/100, workerRunwayMin, workerRunwayMax)
	if runway >= max {
		runway = max - 1
	}
	return runway
}

// Soft sizing ratios for prompt/skill hints (not enforced).
const (
	huntWaveInFlightPercent   = 25
	throwawayBudgetMinPercent = 10
	throwawayBudgetMaxPercent = 20
	deepBudgetRaisePercent    = 150
)

// roundPercent returns n*pct/100 rounded half-up.
func roundPercent(n, pct int) int {
	if n <= 0 || pct <= 0 {
		return 0
	}
	return (n*pct + 50) / 100
}

func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if hi > 0 && n > hi {
		return hi
	}
	return n
}

// SizingHintVars derives soft wave/budget recommendation ints from effective caps.
func SizingHintVars(maxInFlight int, budget WorkerToolBudget) map[string]any {
	if maxInFlight <= 0 {
		maxInFlight = MaxInFlightTaskWorkers
	}
	huntWave := roundPercent(maxInFlight, huntWaveInFlightPercent)
	if huntWave < 1 {
		huntWave = 1
	}
	if huntWave > maxInFlight {
		huntWave = maxInFlight
	}
	throwLo := clampInt(roundPercent(budget.Default, throwawayBudgetMinPercent), budget.Min, budget.Max)
	throwHi := clampInt(roundPercent(budget.Default, throwawayBudgetMaxPercent), budget.Min, budget.Max)
	if throwHi < throwLo {
		throwHi = throwLo
	}
	deepHint := clampInt(roundPercent(budget.Default, deepBudgetRaisePercent), budget.Default, budget.Max)
	return map[string]any{
		"hunt_wave_workers":        huntWave,
		"throwaway_tool_loops_min": throwLo,
		"throwaway_tool_loops_max": throwHi,
		"deep_tool_loops_hint":     deepHint,
	}
}

// RefreshSizingHintVars recomputes soft sizing hints from caps already in into.
func RefreshSizingHintVars(into map[string]any) {
	if into == nil {
		return
	}
	maxInFlight := MaxInFlightTaskWorkers
	if v, ok := into["max_in_flight"].(int); ok && v > 0 {
		maxInFlight = v
	}
	budget := DefaultWorkerToolBudget()
	if v, ok := into["worker_tool_budget_default"].(int); ok && v > 0 {
		budget.Default = v
	}
	if v, ok := into["worker_tool_budget_min"].(int); ok && v > 0 {
		budget.Min = v
	}
	if v, ok := into["worker_tool_budget_max"].(int); ok && v > 0 {
		budget.Max = v
	}
	for k, v := range SizingHintVars(maxInFlight, budget) {
		into[k] = v
	}
}

// PolicyTemplateVars returns pongo context for host caps and soft sizing hints.
func PolicyTemplateVars(budget WorkerToolBudget) map[string]any {
	out := map[string]any{
		"max_in_flight":              MaxInFlightTaskWorkers,
		"max_read_workers":           DefaultMaxReadTaskWorkers,
		"max_write_workers":          DefaultMaxWriteTaskWorkers,
		"max_concurrent_tool_calls":  MaxConcurrentToolCalls,
		"worker_read_line_limit":     readcaps.LineLimit,
		"recall_may_widen":           false,
		"worker_summary_max_chars":   limits.DefaultWorkerSummaryMaxChars,
		"worker_tool_budget_default": budget.Default,
		"worker_tool_budget_min":     budget.Min,
		"worker_tool_budget_max":     budget.Max,
	}
	RefreshSizingHintVars(out)
	return out
}
