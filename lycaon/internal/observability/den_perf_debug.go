package observability

import (
	"log/slog"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/debugpaths"
)

// DenPerfDebugEnabled reports whether Den main-thread perf/stall events are
// mirrored to a debug JSONL file.
func DenPerfDebugEnabled() bool {
	return debugpaths.Enabled(debugpaths.KindDenPerf)
}

// DenPerfDebugEntry is one Den client-side perf line (freeze/stall attribution).
type DenPerfDebugEntry struct {
	Time    time.Time      `json:"ts"`
	Channel string         `json:"channel,omitempty"`
	Event   string         `json:"event"`
	Detail  map[string]any `json:"detail,omitempty"`
}

var (
	denPerfDebugOnce sync.Once
	denPerfDebug     *jsonlDebugLog
)

func initDenPerfDebugLog() {
	if !DenPerfDebugEnabled() {
		return
	}
	log, err := openJSONLDebugLog(true, debugpaths.KindDenPerf)
	if err != nil {
		slog.Warn("den perf debug logging disabled", "err", err)
		return
	}
	denPerfDebug = log
	slog.Info("den perf debug logging enabled", "path", log.path)
}

func activeDenPerfDebugLog() *jsonlDebugLog {
	denPerfDebugOnce.Do(initDenPerfDebugLog)
	return denPerfDebug
}

// LogDenPerfEvent appends one Den client perf event when the capture is on.
func LogDenPerfEvent(entry DenPerfDebugEntry) {
	if entry.Event == "" {
		return
	}
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}
	if entry.Channel == "" {
		entry.Channel = "perf"
	}
	activeDenPerfDebugLog().write(entry)
}
