package watchdiag

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/debugretention"
	"github.com/lycaon/lycaon/internal/observability"
)

func init() {
	observability.RegisterDebugCloser(Close)
}

// Config configures a bounded asynchronous watch diagnostic observer.
type Config struct {
	Enabled        bool
	QueueCapacity  int
	ReportCapacity int
	TaskTimeout    time.Duration
	CloseGrace     time.Duration
	Sink           func(Finding)
	Now            func() time.Time
}

// Stats summarizes bounded queue, deduplication, and local report activity.
type Stats struct {
	Enqueued     int64
	Dropped      int64
	Emitted      int64
	Deduplicated int64
	Evicted      int64
}

type workItem struct {
	obs  *Observation
	task *Task
}

type dedupKey struct {
	rule         RuleID
	sessionID    string
	watchID      string
	handle       string
	occurrenceID string
	claimID      string
	turnID       string
}

// Observer evaluates wait/watch observations asynchronously on a bounded queue
// and maintains a bounded deduplicated local report. Submit and SubmitTask
// never block callers and record drops explicitly.
type Observer struct {
	enabled        bool
	capacity       int
	reportCapacity int
	taskTimeout    time.Duration
	closeGrace     time.Duration
	sink           func(Finding)
	now            func() time.Time
	file           *debugretention.File

	workerCtx    context.Context
	workerCancel context.CancelFunc

	mu               sync.RWMutex
	closed           bool
	queue            chan workItem
	wg               sync.WaitGroup
	closeOnce        sync.Once
	seen             map[dedupKey]int
	reportMu         sync.RWMutex
	report           []Finding
	enqueued         atomic.Int64
	dropped          atomic.Int64
	emitted          atomic.Int64
	deduplicated     atomic.Int64
	evicted          atomic.Int64
	reportedDropMark atomic.Int64
}

// NewObserver creates a bounded async observer. When cfg.Enabled is false,
// no background goroutine is started and Submit/SubmitTask are pure no-ops.
func NewObserver(cfg Config) *Observer {
	if !cfg.Enabled {
		return &Observer{enabled: false}
	}
	capacity := cfg.QueueCapacity
	if capacity <= 0 {
		capacity = DefaultQueueCapacity
	}
	reportCap := cfg.ReportCapacity
	if reportCap <= 0 {
		reportCap = DefaultReportCapacity
	}
	timeout := cfg.TaskTimeout
	if timeout <= 0 {
		timeout = DefaultTaskTimeout
	}
	grace := cfg.CloseGrace
	if grace <= 0 {
		grace = DefaultCloseGrace
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	workerCtx, workerCancel := context.WithCancel(context.Background())
	obs := &Observer{
		enabled:        true,
		capacity:       capacity,
		reportCapacity: reportCap,
		taskTimeout:    timeout,
		closeGrace:     grace,
		sink:           cfg.Sink,
		now:            nowFn,
		workerCtx:      workerCtx,
		workerCancel:   workerCancel,
		queue:          make(chan workItem, capacity),
		seen:           make(map[dedupKey]int),
		report:         make([]Finding, 0, min(reportCap, 16)),
	}
	obs.wg.Add(1)
	go obs.run()
	return obs
}

// Submit enqueues an immutable observation without blocking.
func (o *Observer) Submit(obs Observation) bool {
	if o == nil || !o.enabled {
		return false
	}
	copyObs := obs
	return o.enqueue(workItem{obs: &copyObs})
}

// SubmitTask enqueues a deferred fetch task without blocking. When the queue
// is saturated or the observer is disabled, task.Fetch is never called.
func (o *Observer) SubmitTask(task Task) bool {
	if o == nil || !o.enabled || task.Fetch == nil {
		return false
	}
	copyTask := task
	return o.enqueue(workItem{task: &copyTask})
}

func (o *Observer) enqueue(item workItem) bool {
	if o == nil || !o.enabled {
		return false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.closed {
		return false
	}
	select {
	case o.queue <- item:
		o.enqueued.Add(1)
		return true
	default:
		o.dropped.Add(1)
		return false
	}
}

// Stats returns the current enqueued, dropped, emitted, deduplicated, and
// evicted counts.
func (o *Observer) Stats() Stats {
	if o == nil {
		return Stats{}
	}
	return Stats{
		Enqueued:     o.enqueued.Load(),
		Dropped:      o.dropped.Load(),
		Emitted:      o.emitted.Load(),
		Deduplicated: o.deduplicated.Load(),
		Evicted:      o.evicted.Load(),
	}
}

// Report returns a bounded, deduplicated snapshot of retained local findings.
func (o *Observer) Report() LocalReport {
	if o == nil || !o.enabled {
		return LocalReport{SchemaVersion: SchemaVersion}
	}
	o.reportMu.RLock()
	findings := make([]Finding, len(o.report))
	copy(findings, o.report)
	o.reportMu.RUnlock()

	return LocalReport{
		SchemaVersion:     SchemaVersion,
		GeneratedAt:       o.now().UTC(),
		Capacity:          o.reportCapacity,
		TotalFindings:     len(findings),
		DeduplicatedCount: o.deduplicated.Load(),
		DroppedCount:      o.dropped.Load(),
		EvictedCount:      o.evicted.Load(),
		Findings:          findings,
	}
}

// Close stops accepting new work, cancels any in-flight fetch after the close
// grace window, drains queued work, records unreported overflow drops, and
// closes owned files.
func (o *Observer) Close() {
	if o == nil || !o.enabled {
		return
	}
	o.closeOnce.Do(func() {
		o.mu.Lock()
		o.closed = true
		close(o.queue)
		o.mu.Unlock()
		done := make(chan struct{})
		go func() {
			o.wg.Wait()
			close(done)
		}()
		timer := time.NewTimer(o.closeGrace)
		select {
		case <-done:
			timer.Stop()
		case <-timer.C:
			if o.workerCancel != nil {
				o.workerCancel()
			}
			<-done
		}
		if o.workerCancel != nil {
			o.workerCancel()
		}
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
		var obs Observation
		if item.task != nil {
			taskCtx, cancel := context.WithTimeout(o.workerCtx, o.taskTimeout)
			fetched, err := item.task.Fetch(taskCtx)
			cancel()
			if err != nil {
				o.emitDedup(sanitizeFinding(Finding{
					SchemaVersion: SchemaVersion,
					Timestamp:     o.now().UTC(),
					RuleID:        RuleObservationLoss,
					Disposition:   DispositionIncomplete,
					SessionID:     item.task.SessionID,
					WatchID:       item.task.WatchID,
					Handle:        item.task.Handle,
					Expected:      map[string]any{"watch_snapshot_read_succeeded": true},
					Observed:      map[string]any{"watch_snapshot_read_succeeded": false},
					Reason:        "watch_snapshot_read_failed",
				}))
				continue
			}
			obs = fetched
		} else if item.obs != nil {
			obs = *item.obs
		}
		for _, finding := range Evaluate(obs, o.now()) {
			o.emitDedup(finding)
		}
	}
}

func dispositionRank(d Disposition) int {
	if d == DispositionContradiction {
		return 2
	}
	return 1
}

func findingDedupKey(f Finding) (dedupKey, bool) {
	if f.RuleID == RuleCaptureOverflow {
		return dedupKey{}, false
	}
	key := dedupKey{
		rule:         f.RuleID,
		sessionID:    f.SessionID,
		watchID:      f.WatchID,
		handle:       f.Handle,
		occurrenceID: f.OccurrenceID,
		claimID:      f.ClaimID,
		turnID:       f.TurnID,
	}
	if key.sessionID == "" && key.watchID == "" && key.handle == "" &&
		key.occurrenceID == "" && key.claimID == "" && key.turnID == "" {
		return dedupKey{}, false
	}
	return key, true
}

func (o *Observer) emitDedup(f Finding) {
	if key, ok := findingDedupKey(f); ok {
		rank := dispositionRank(f.Disposition)
		if prevRank, exists := o.seen[key]; exists && rank <= prevRank {
			o.deduplicated.Add(1)
			return
		}
		if len(o.seen) >= DefaultMaxDedupEntries {
			clear(o.seen)
		}
		o.seen[key] = rank
	}
	o.emit(f)
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
	o.appendReport(f)
	o.emitted.Add(1)
	if o.sink != nil {
		o.sink(f)
	}
}

func (o *Observer) appendReport(f Finding) {
	o.reportMu.Lock()
	defer o.reportMu.Unlock()
	if len(o.report) >= o.reportCapacity {
		copy(o.report, o.report[1:])
		o.report[len(o.report)-1] = f
		o.evicted.Add(1)
		return
	}
	o.report = append(o.report, f)
}
