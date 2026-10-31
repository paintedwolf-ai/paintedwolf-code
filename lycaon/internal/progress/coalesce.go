package progress

import (
	"strings"
	"sync"
	"time"
)

// DefaultCoalesceWindow is the quiet period after the last update_progress write before a coalesced
// change message is emitted. Successive writes inside the window collapse into one chat row.
const DefaultCoalesceWindow = time.Second

// FlushPayload is the coalesced delta handed to the emit callback: the snapshot at the start of
// the window (Baseline) and the most recent snapshot (Latest), plus a per-session ordering Seq.
// ChangedAt is when the write produced Latest, so the transcript row anchors
// to when the plan actually changed rather than the debounced flush instant (which can trail the
// coordinator's synthesis prose and sort the row to the bottom).
type FlushPayload struct {
	SessionID string
	Baseline  Snapshot
	Latest    Snapshot
	Seq       int
	ChangedAt time.Time
}

// FlushFunc receives a coalesced window when it settles.
type FlushFunc func(payload FlushPayload)

// Coalescer debounces per-session update_progress writes so a burst of edits drops a single change
// row into chat. Each session orders notifications by committed DocumentVersion and serializes
// emit callbacks per session so concurrent flushes cannot deliver out of order.
type Coalescer struct {
	mu               sync.Mutex
	closeOnce        sync.Once
	closed           bool
	emits            sync.WaitGroup
	window           time.Duration
	emit             FlushFunc
	now              func() time.Time
	pending          map[string]*coalesceWindow
	seq              map[string]int
	publishedVersion map[string]uint64
	emitQueue        map[string][]*coalesceEmitItem
	emitting         map[string]bool
}

type coalesceWindow struct {
	baseline  Snapshot
	latest    Snapshot
	changedAt time.Time
	timer     *time.Timer
}

type coalesceEmitItem struct {
	payload FlushPayload
	done    chan struct{}
}

// NewCoalescer uses DefaultCoalesceWindow when window is not positive.
func NewCoalescer(window time.Duration, emit FlushFunc) *Coalescer {
	if window <= 0 {
		window = DefaultCoalesceWindow
	}
	return &Coalescer{
		window:           window,
		emit:             emit,
		now:              func() time.Time { return time.Now().UTC() },
		pending:          make(map[string]*coalesceWindow),
		seq:              make(map[string]int),
		publishedVersion: make(map[string]uint64),
		emitQueue:        make(map[string][]*coalesceEmitItem),
		emitting:         make(map[string]bool),
	}
}

// Record folds one committed transition into the session's open window, ordering by DocumentVersion.
func (c *Coalescer) Record(sessionID string, before, after Snapshot) {
	key := strings.TrimSpace(sessionID)
	if key == "" || c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	afterVer := after.DocumentVersion()
	beforeVer := before.DocumentVersion()
	if afterVer > 0 && afterVer <= c.publishedVersion[key] {
		return
	}
	w := c.pending[key]
	if w == nil {
		w = &coalesceWindow{
			baseline:  before,
			latest:    after,
			changedAt: c.now(),
		}
		c.pending[key] = w
	} else {
		if afterVer > 0 && w.latest.DocumentVersion() > 0 {
			if beforeVer < w.baseline.DocumentVersion() {
				w.baseline = before
			}
			if afterVer > w.latest.DocumentVersion() {
				w.latest = after
				w.changedAt = c.now()
			}
		} else {
			w.latest = after
			w.changedAt = c.now()
		}
	}
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(c.window, func() { c.flush(key) })
}

// FlushNow settles a session's window immediately, bypassing the timer. Used by tests and shutdown.
func (c *Coalescer) FlushNow(sessionID string) {
	c.flush(strings.TrimSpace(sessionID))
}

func (c *Coalescer) flush(key string) {
	if key == "" || c == nil {
		return
	}
	c.mu.Lock()
	w := c.pending[key]
	if w == nil {
		var wait chan struct{}
		if queue := c.emitQueue[key]; len(queue) > 0 {
			wait = queue[len(queue)-1].done
		}
		c.mu.Unlock()
		if wait != nil {
			<-wait
		}
		return
	}
	delete(c.pending, key)
	if w.timer != nil {
		w.timer.Stop()
	}
	if ver := w.latest.DocumentVersion(); ver > c.publishedVersion[key] {
		c.publishedVersion[key] = ver
	}
	c.seq[key]++
	payload := FlushPayload{
		SessionID: key,
		Baseline:  w.baseline,
		Latest:    w.latest,
		Seq:       c.seq[key],
		ChangedAt: w.changedAt,
	}
	if c.emit == nil {
		c.mu.Unlock()
		return
	}
	item := &coalesceEmitItem{
		payload: payload,
		done:    make(chan struct{}),
	}
	c.emitQueue[key] = append(c.emitQueue[key], item)
	if c.emitting[key] {
		c.mu.Unlock()
		<-item.done
		return
	}
	c.emitting[key] = true
	c.emits.Add(1)
	c.mu.Unlock()

	defer c.emits.Done()
	for {
		c.mu.Lock()
		queue := c.emitQueue[key]
		if len(queue) == 0 {
			delete(c.emitQueue, key)
			delete(c.emitting, key)
			c.mu.Unlock()
			return
		}
		next := queue[0]
		emit := c.emit
		c.mu.Unlock()

		if emit != nil {
			emit(next.payload)
		}
		close(next.done)

		c.mu.Lock()
		c.emitQueue[key] = c.emitQueue[key][1:]
		if len(c.emitQueue[key]) == 0 {
			delete(c.emitQueue, key)
			delete(c.emitting, key)
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
	}
}

// Close seals new writes, delivers pending windows, and drains emit callbacks.
func (c *Coalescer) Close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		keys := make([]string, 0, len(c.pending))
		for key, window := range c.pending {
			if window.timer != nil {
				window.timer.Stop()
			}
			keys = append(keys, key)
		}
		c.mu.Unlock()
		for _, key := range keys {
			c.flush(key)
		}
		c.emits.Wait()
		c.mu.Lock()
		c.emit = nil
		c.mu.Unlock()
	})
}
