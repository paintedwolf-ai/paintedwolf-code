package observability

import "time"

// slowLatencyInfoMs promotes slow single-shot latency lines from Debug to Info.
const slowLatencyInfoMs = 500

// LogLatency emits one human-readable latency line. Structured performance
// capture is handled separately so ordinary logs stay concise.
func LogLatency(component, msg string, start time.Time, attrs ...any) {
	elapsed := time.Since(start).Milliseconds()
	attrs = append(attrs, "duration_ms", elapsed)
	log := LazyComponent(component)
	if elapsed >= slowLatencyInfoMs {
		log.Info(msg, attrs...)
		return
	}
	log.Debug(msg, attrs...)
}
