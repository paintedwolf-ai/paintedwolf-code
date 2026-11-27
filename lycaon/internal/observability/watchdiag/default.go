package watchdiag

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
	overrideObs     *Observer
)

// Enabled reports whether opt-in dev watch diagnostics are active.
func Enabled() bool {
	defaultMu.Lock()
	override := overrideObs
	defaultMu.Unlock()
	if override != nil {
		return override.enabled
	}
	return debugpaths.Enabled(debugpaths.KindWatchDiagnostics)
}

// Observe submits an immutable observation when enabled.
func Observe(obs Observation) bool {
	if target := activeObserver(); target != nil {
		return target.Submit(obs)
	}
	return false
}

// ObserveTask submits a deferred bounded read task when enabled.
func ObserveTask(task Task) bool {
	if target := activeObserver(); target != nil {
		return target.SubmitTask(task)
	}
	return false
}

// Report returns the bounded deduplicated local report from the active observer,
// or an empty report when diagnostics are disabled.
func Report() LocalReport {
	if target := activeObserver(); target != nil {
		return target.Report()
	}
	return LocalReport{SchemaVersion: SchemaVersion}
}

func activeObserver() *Observer {
	defaultMu.Lock()
	override := overrideObs
	defaultMu.Unlock()
	if override != nil {
		return override
	}
	if !debugpaths.Enabled(debugpaths.KindWatchDiagnostics) {
		return nil
	}
	return ensureDefaultObserver()
}

// SetObserver installs a scoped observer override (intended for tests).
func SetObserver(obs *Observer) func() {
	defaultMu.Lock()
	prev := overrideObs
	overrideObs = obs
	defaultMu.Unlock()
	return func() {
		defaultMu.Lock()
		overrideObs = prev
		defaultMu.Unlock()
	}
}

// Close drains and closes the process-wide dev watch diagnostics observer.
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
	path := debugpaths.FilePath(debugpaths.KindWatchDiagnostics)
	var file *debugretention.File
	if path != "" {
		opened, err := debugretention.OpenFile(path, debugretention.DefaultConfig().MaxFileBytes)
		if err != nil {
			slog.Warn("watch diagnostics log file disabled", "err", err)
		} else {
			file = opened
		}
	}
	var writeMu sync.Mutex
	obs := NewObserver(Config{
		Enabled:        true,
		QueueCapacity:  DefaultQueueCapacity,
		ReportCapacity: DefaultReportCapacity,
		Sink:           func(f Finding) { writeFinding(file, &writeMu, f) },
	})
	obs.file = file
	defaultObserver = obs
	return obs
}

func writeFinding(file *debugretention.File, mu *sync.Mutex, f Finding) {
	if f.Disposition == DispositionContradiction || f.Disposition == DispositionOverflow {
		slog.Warn("watch runtime diagnostic",
			"rule_id", string(f.RuleID),
			"disposition", string(f.Disposition),
			"session_id", f.SessionID,
			"watch_id", f.WatchID,
			"handle", f.Handle,
			"reason", f.Reason,
			"dropped_count", f.DroppedCount,
		)
	}
	if file == nil {
		return
	}
	data, err := json.Marshal(f)
	if err != nil {
		slog.Warn("watch diagnostics marshal failed", "err", err)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if _, err := file.Write(append(data, '\n')); err != nil {
		slog.Warn("watch diagnostics write failed", "err", err)
	}
}
