package cmddiag

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

// Config configures a bounded asynchronous diagnostic observer.
type Config struct {
	Enabled       bool
	QueueCapacity int
	TaskTimeout   time.Duration
	CloseGrace    time.Duration
	Sink          func(Finding)
	Now           func() time.Time
}

// Stats summarizes bounded queue activity for an observer.
type Stats struct {
	Enqueued     int64
	Dropped      int64
	Emitted      int64
	Deduplicated int64
}

type workItem struct {
	obs  *Observation
	task *Task
}

type dedupKey struct {
	rule      RuleID
	programID string
}

// Observer evaluates command-program observations asynchronously on a bounded
// queue. Submit and SubmitTask never block callers and record drops explicitly.
type Observer struct {
	enabled     bool
	capacity    int
	taskTimeout time.Duration
	closeGrace  time.Duration
	sink        func(Finding)
	now         func() time.Time
	file        *debugretention.File

	workerCtx    context.Context
	workerCancel context.CancelFunc

	mu               sync.RWMutex
	closed           bool
	queue            chan workItem
	wg               sync.WaitGroup
	closeOnce        sync.Once
	seen             map[dedupKey]struct{}
	enqueued         atomic.Int64
	dropped          atomic.Int64
	emitted          atomic.Int64
	deduplicated     atomic.Int64
	reportedDropMark atomic.Int64
}

// NewObserver creates a bounded async observer. When cfg.Enabled is false,
// no background goroutine is started and Submit/SubmitTask are no-ops.
func NewObserver(cfg Config) *Observer {
	if !cfg.Enabled {
		return &Observer{enabled: false}
	}
	capacity := cfg.QueueCapacity
	if capacity <= 0 {
		capacity = DefaultQueueCapacity
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
		enabled:      true,
		capacity:     capacity,
		taskTimeout:  timeout,
		closeGrace:   grace,
		sink:         cfg.Sink,
		now:          nowFn,
		workerCtx:    workerCtx,
		workerCancel: workerCancel,
		queue:        make(chan workItem, capacity),
		seen:         make(map[dedupKey]struct{}),
	}
	obs.wg.Add(1)
	go obs.run()
	return obs
}

// Submit enqueues an immutable observation without blocking.
func (o *Observer) Submit(obs Observation) bool {
	copyObs := obs
	return o.enqueue(workItem{obs: &copyObs})
}

// SubmitTask enqueues a deferred fetch task without blocking. When the queue
// is saturated, task.Fetch is never called.
func (o *Observer) SubmitTask(task Task) bool {
	if task.Fetch == nil {
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

// Stats returns the current enqueued, dropped, emitted, and deduplicated counts.
func (o *Observer) Stats() Stats {
	if o == nil {
		return Stats{}
	}
	return Stats{
		Enqueued:     o.enqueued.Load(),
		Dropped:      o.dropped.Load(),
		Emitted:      o.emitted.Load(),
		Deduplicated: o.deduplicated.Load(),
	}
}

// Close stops accepting new work, cancels any in-flight fetch, drains queued
// work, records unreported overflow drops, and closes owned files.
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
					ProgramID:     item.task.ProgramID,
					ToolCallID:    item.task.ToolCallID,
					Expected:      map[string]any{"receipt_read_succeeded": true},
					Observed:      map[string]any{"receipt_read_succeeded": false},
					Reason:        "receipt_read_failed",
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

func (o *Observer) emitDedup(f Finding) {
	if f.RuleID != RuleCaptureOverflow && f.ProgramID != "" {
		key := dedupKey{rule: f.RuleID, programID: f.ProgramID}
		if _, exists := o.seen[key]; exists {
			o.deduplicated.Add(1)
			return
		}
		if len(o.seen) >= DefaultMaxDedupEntries {
			clear(o.seen)
		}
		o.seen[key] = struct{}{}
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
	o.emitted.Add(1)
	if o.sink != nil {
		o.sink(f)
	}
}
