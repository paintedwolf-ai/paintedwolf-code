package cmddiag

import (
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/debugretention"
	"github.com/lycaon/lycaon/internal/observability"
)

func init() {
	observability.RegisterDebugCloser(Close)
}

// Config configures a bounded asynchronous diagnostic observer.
type Config struct {
	Enabled       bool
	QueueCapacity int
	Sink          func(Finding)
	Now           func() time.Time
}

// Stats summarizes bounded queue activity for an observer.
type Stats struct {
	Enqueued int64
	Dropped  int64
	Emitted  int64
}

// Observer evaluates command-program observations asynchronously on a bounded
// queue. Submit never blocks callers and records drops explicitly.
type Observer struct {
	enabled  bool
	capacity int
	sink     func(Finding)
	now      func() time.Time
	file     *debugretention.File

	mu               sync.RWMutex
	closed           bool
	queue            chan Observation
	wg               sync.WaitGroup
	closeOnce        sync.Once
	enqueued         atomic.Int64
	dropped          atomic.Int64
	emitted          atomic.Int64
	reportedDropMark atomic.Int64
}

// NewObserver creates a bounded async observer. When cfg.Enabled is false,
// no background goroutine is started and Submit is a no-op.
func NewObserver(cfg Config) *Observer {
	if !cfg.Enabled {
		return &Observer{enabled: false}
	}
	capacity := cfg.QueueCapacity
	if capacity <= 0 {
		capacity = DefaultQueueCapacity
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	obs := &Observer{
		enabled:  true,
		capacity: capacity,
		sink:     cfg.Sink,
		now:      nowFn,
		queue:    make(chan Observation, capacity),
	}
	obs.wg.Add(1)
	go obs.run()
	return obs
}

// Submit enqueues an observation without blocking. It returns false when the
// observer is disabled, closed, or its bounded queue is saturated.
func (o *Observer) Submit(obs Observation) bool {
	if o == nil || !o.enabled {
		return false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.closed {
		return false
	}
	select {
	case o.queue <- obs:
		o.enqueued.Add(1)
		return true
	default:
		o.dropped.Add(1)
		return false
	}
}

// Stats returns the current enqueued, dropped, and emitted counters.
func (o *Observer) Stats() Stats {
	if o == nil {
		return Stats{}
	}
	return Stats{
		Enqueued: o.enqueued.Load(),
		Dropped:  o.dropped.Load(),
		Emitted:  o.emitted.Load(),
	}
}

// Close stops accepting new observations, drains queued items, records any
// unreported overflow drops, and closes the underlying capture file if owned.
func (o *Observer) Close() {
	if o == nil || !o.enabled {
		return
	}
	o.closeOnce.Do(func() {
		o.mu.Lock()
		o.closed = true
		close(o.queue)
		o.mu.Unlock()
		o.wg.Wait()
		o.emitOverflowIfNeeded()
		if o.file != nil {
			_ = o.file.Close()
			o.file = nil
		}
	})
}

func (o *Observer) run() {
	defer o.wg.Done()
	for item := range o.queue {
		o.emitOverflowIfNeeded()
		for _, finding := range Evaluate(item, o.now()) {
			o.emit(finding)
		}
	}
}

func (o *Observer) emitOverflowIfNeeded() {
	dropped := o.dropped.Load()
	prev := o.reportedDropMark.Load()
	if dropped <= prev {
		return
	}
	if !o.reportedDropMark.CompareAndSwap(prev, dropped) {
		return
	}
	f := sanitizeFinding(Finding{
		SchemaVersion: SchemaVersion,
		Timestamp:     o.now().UTC(),
		RuleID:        RuleCaptureOverflow,
		Disposition:   DispositionOverflow,
		DroppedCount:  dropped,
		Expected:      map[string]any{"dropped_observations": 0, "queue_capacity": o.capacity},
		Observed:      map[string]any{"dropped_observations": dropped},
		Reason:        "bounded_queue_saturated",
	})
	o.emit(f)
}

func (o *Observer) emit(f Finding) {
	o.emitted.Add(1)
	if o.sink != nil {
		o.sink(f)
	}
}

var (
	defaultMu       sync.Mutex
	defaultObserver *Observer
	overrideObs     *Observer
)

// Enabled reports whether opt-in dev command diagnostics are active via
// LYCAON_DEV_COMMAND_DIAGNOSTICS (or LYCAON_DEBUG_ALL) or a test override.
func Enabled() bool {
	defaultMu.Lock()
	override := overrideObs
	defaultMu.Unlock()
	if override != nil {
		return override.enabled
	}
	return debugpaths.Enabled(debugpaths.KindCommandDiagnostics)
}

// Observe submits an immutable observation to the active dev diagnostics
// observer when enabled. When disabled it returns false with zero side effects.
func Observe(obs Observation) bool {
	defaultMu.Lock()
	override := overrideObs
	defaultMu.Unlock()
	if override != nil {
		return override.Submit(obs)
	}
	if !debugpaths.Enabled(debugpaths.KindCommandDiagnostics) {
		return false
	}
	return ensureDefaultObserver().Submit(obs)
}

// SetObserver installs a scoped observer override (intended for tests) and
// returns a restore function.
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
		Sink: func(f Finding) {
			writeFinding(file, &writeMu, f)
		},
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
