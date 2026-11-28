package cmddiag

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/debugretention"
)

var (
	defaultMu       sync.Mutex
	defaultObserver *Observer
)

// Enabled reports whether opt-in dev command diagnostics are active.
func Enabled() bool {
	return debugpaths.Enabled(debugpaths.KindCommandDiagnostics)
}

// DefaultObserver returns the process-wide observer when diagnostics are enabled.
func DefaultObserver() *Observer {
	if !Enabled() {
		return nil
	}
	return ensureDefaultObserver()
}

// Close drains and closes the process-wide dev command diagnostics observer.
func Close() {
	defaultMu.Lock()
	obs := defaultObserver
	defaultObserver = nil
	defaultMu.Unlock()
	if obs != nil {
		obs.Close()
	}
}

func ensureDefaultObserver() *Observer {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultObserver != nil {
		return defaultObserver
	}
	path := debugpaths.FilePath(debugpaths.KindCommandDiagnostics)
	var file *debugretention.File
	if path != "" {
		opened, err := debugretention.OpenFile(path, debugretention.DefaultConfig().MaxFileBytes)
		if err != nil {
			slog.Warn("command diagnostics log file disabled", "err", err)
		} else {
			file = opened
		}
	}
	var writeMu sync.Mutex
	obs := NewObserver(Config{
		Enabled:       true,
		QueueCapacity: DefaultQueueCapacity,
		Sink:          func(f Finding) { writeFinding(file, &writeMu, f) },
	})
	obs.file = file
	defaultObserver = obs
	return obs
}

func writeFinding(file *debugretention.File, mu *sync.Mutex, f Finding) {
	if f.Disposition == DispositionContradiction || f.Disposition == DispositionOverflow {
		slog.Warn("command runtime diagnostic",
			"rule_id", string(f.RuleID),
			"disposition", string(f.Disposition),
			"session_id", f.SessionID,
			"program_id", f.ProgramID,
			"reason", f.Reason,
			"dropped_count", f.DroppedCount,
		)
	}
	if file == nil {
		return
	}
	data, err := json.Marshal(f)
	if err != nil {
		slog.Warn("command diagnostics marshal failed", "err", err)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if _, err := file.Write(append(data, '\n')); err != nil {
		slog.Warn("command diagnostics write failed", "err", err)
	}
}
