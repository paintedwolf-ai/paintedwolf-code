package spawn

import (
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
)

// Host caps are surfaced in prompts and enforced at runtime.
const MaxConcurrentToolCalls = 64

// huntWaveInFlightPercent sizes the read-worker wave a broad survey suggests.
const huntWaveInFlightPercent = 25

// SizingHintVars derives the soft wave size prompts suggest from the in-flight cap.
func SizingHintVars(maxInFlight int) map[string]any {
	if maxInFlight <= 0 {
		maxInFlight = MaxInFlightTaskWorkers
	}
	huntWave := min(max((maxInFlight*huntWaveInFlightPercent+50)/100, 1), maxInFlight)
	return map[string]any{"hunt_wave_workers": huntWave}
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
	for k, v := range SizingHintVars(maxInFlight) {
		into[k] = v
	}
}

// PolicyTemplateVars returns pongo context for host caps and soft sizing hints.
func PolicyTemplateVars() map[string]any {
	out := map[string]any{
		"max_in_flight":             MaxInFlightTaskWorkers,
		"max_read_workers":          DefaultMaxReadTaskWorkers,
		"max_write_workers":         DefaultMaxWriteTaskWorkers,
		"max_concurrent_tool_calls": MaxConcurrentToolCalls,
		"worker_read_line_limit":    readcaps.LineLimit,
		"recall_may_widen":          false,
		"worker_summary_max_chars":  limits.DefaultWorkerSummaryMaxChars,
	}
	RefreshSizingHintVars(out)
	return out
}
