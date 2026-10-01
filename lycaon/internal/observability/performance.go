package observability

import (
	"context"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/debugpaths"
)

const (
	performanceQueueSize    = 2048
	performanceSamplePeriod = time.Second
)

// PerformanceEnabled reports whether metadata-only performance capture is active.
func PerformanceEnabled() bool {
	return debugpaths.Enabled(debugpaths.KindPerformance)
}

// PerformancePhase is one named interval within an operation.
type PerformancePhase struct {
	Name       string `json:"name"`
	DurationUS int64  `json:"duration_us"`
}

// PerformanceRecord is one operation or runtime sample in performance.jsonl.
// Dimensions contain bounded identities and workload shape, never payloads.
type PerformanceRecord struct {
	Time       time.Time          `json:"ts"`
	Kind       string             `json:"kind"`
	Operation  string             `json:"operation,omitempty"`
	Outcome    string             `json:"outcome,omitempty"`
	DurationUS int64              `json:"duration_us,omitempty"`
	Phases     []PerformancePhase `json:"phases,omitempty"`
	Dimensions map[string]string  `json:"dimensions,omitempty"`
	Runtime    *RuntimeSample     `json:"runtime,omitempty"`
}

// RuntimeSample captures process-local resources plus optional subsystem gauges.
// RSS is sampled by the external performance harness because Go does not expose
// portable current-resident memory.
type RuntimeSample struct {
	HeapAllocBytes  uint64           `json:"heap_alloc_bytes"`
	HeapInUseBytes  uint64           `json:"heap_in_use_bytes"`
	HeapObjects     uint64           `json:"heap_objects"`
	StackInUseBytes uint64           `json:"stack_in_use_bytes"`
	Goroutines      int              `json:"goroutines"`
	GCCount         uint32           `json:"gc_count"`
	GCPauseTotalNS  uint64           `json:"gc_pause_total_ns"`
	Gauges          map[string]int64 `json:"gauges,omitempty"`
	RecordsDropped  uint64           `json:"records_dropped,omitempty"`
}

type performanceCapture struct {
	log     *jsonlDebugLog
	records chan PerformanceRecord
	done    chan struct{}
	mu      sync.RWMutex
	closed  atomic.Bool
	dropped atomic.Uint64
}

var (
	performanceOnce sync.Once
	performanceLog  *performanceCapture
)

func activePerformanceCapture() *performanceCapture {
	performanceOnce.Do(func() {
		if !PerformanceEnabled() {
			return
		}
		log, err := openJSONLDebugLog(true, debugpaths.KindPerformance)
		if err != nil {
			slog.Warn("performance capture disabled", "err", err)
			return
		}
		capture := &performanceCapture{
			log: log, records: make(chan PerformanceRecord, performanceQueueSize), done: make(chan struct{}),
		}
		performanceLog = capture
		go capture.writeLoop()
		slog.Info("performance capture enabled", "path", log.path)
	})
	return performanceLog
}

func (c *performanceCapture) writeLoop() {
	defer close(c.done)
	for record := range c.records {
		c.log.write(record)
	}
	if dropped := c.dropped.Load(); dropped > 0 {
		c.log.write(PerformanceRecord{
			Time: time.Now().UTC(), Kind: "capture", Outcome: "dropped",
			Runtime: &RuntimeSample{RecordsDropped: dropped},
		})
	}
}

func (c *performanceCapture) record(record PerformanceRecord) {
	if c == nil {
		return
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed.Load() {
		return
	}
	select {
	case c.records <- record:
	default:
		c.dropped.Add(1)
	}
}

func (c *performanceCapture) close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.closed.CompareAndSwap(false, true) {
		c.mu.Unlock()
		return
	}
	close(c.records)
	c.mu.Unlock()
	<-c.done
	c.log.close()
}

// PerformanceOperation measures one domain operation with optional phases.
type PerformanceOperation struct {
	capture    *performanceCapture
	name       string
	started    time.Time
	last       time.Time
	dimensions map[string]string
	phases     []PerformancePhase
	ended      bool
}

// StartPerformanceOperation begins a performance record. The disabled path
// performs no allocation and returns nil.
func StartPerformanceOperation(name string, dimensions map[string]string) *PerformanceOperation {
	capture := activePerformanceCapture()
	if capture == nil {
		return nil
	}
	now := time.Now()
	return &PerformanceOperation{
		capture: capture, name: name, started: now, last: now,
		dimensions: cloneDimensions(dimensions),
	}
}

// Mark closes the current phase and starts the next one.
func (o *PerformanceOperation) Mark(name string) {
	if o == nil || o.ended {
		return
	}
	now := time.Now()
	o.phases = append(o.phases, PerformancePhase{Name: name, DurationUS: now.Sub(o.last).Microseconds()})
	o.last = now
}

// SetDimension adds or replaces one bounded operation dimension.
func (o *PerformanceOperation) SetDimension(name, value string) {
	if o == nil || o.ended {
		return
	}
	if o.dimensions == nil {
		o.dimensions = make(map[string]string)
	}
	o.dimensions[name] = value
}

// End records the operation. Outcome should be a bounded state such as ok,
// rejected, canceled, or error.
func (o *PerformanceOperation) End(outcome string) {
	if o == nil || o.ended {
		return
	}
	o.ended = true
	finished := time.Now()
	o.capture.record(PerformanceRecord{
		Time: finished.UTC(), Kind: "operation", Operation: o.name, Outcome: outcome,
		DurationUS: finished.Sub(o.started).Microseconds(), Phases: o.phases, Dimensions: o.dimensions,
	})
}

func cloneDimensions(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// RecordRuntimeSample writes one resource snapshot.
func RecordRuntimeSample(gauges map[string]int64) {
	capture := activePerformanceCapture()
	if capture == nil {
		return
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	capture.record(PerformanceRecord{
		Time: time.Now().UTC(), Kind: "runtime",
		Runtime: &RuntimeSample{
			HeapAllocBytes: mem.HeapAlloc, HeapInUseBytes: mem.HeapInuse,
			HeapObjects: mem.HeapObjects, StackInUseBytes: mem.StackInuse,
			Goroutines: runtime.NumGoroutine(), GCCount: mem.NumGC,
			GCPauseTotalNS: mem.PauseTotalNs, Gauges: cloneGauges(gauges),
		},
	})
}

func cloneGauges(in map[string]int64) map[string]int64 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int64, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// StartRuntimeSampler records resources until ctx or the returned function stops it.
func StartRuntimeSampler(ctx context.Context, provider func() map[string]int64) func() {
	if activePerformanceCapture() == nil {
		return func() {}
	}
	sampleCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	RecordRuntimeSample(runtimeGauges(provider))
	go func() {
		defer close(done)
		ticker := time.NewTicker(performanceSamplePeriod)
		defer ticker.Stop()
		for {
			select {
			case <-sampleCtx.Done():
				RecordRuntimeSample(runtimeGauges(provider))
				return
			case <-ticker.C:
				RecordRuntimeSample(runtimeGauges(provider))
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func runtimeGauges(provider func() map[string]int64) map[string]int64 {
	if provider == nil {
		return nil
	}
	return provider()
}

func closePerformance() {
	performanceLog.close()
	performanceOnce = sync.Once{}
	performanceLog = nil
}
