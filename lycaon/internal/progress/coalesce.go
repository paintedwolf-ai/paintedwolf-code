package progress

import (
	"strings"
	"sync"
	"time"
)

// DefaultCoalesceWindow is the quiet period after the last update_progress write before a coalesced
// change message is emitted. Successive writes inside the window collapse into one chat row.
const DefaultCoalesceWindow = time.Second

// FlushPayload is the coalesced delta handed to the emit callback: the plan content at the start of
// the window (Baseline) and the most recent content (Latest), plus a per-session ordering Seq.
// ChangedAt is when the write produced Latest, so the transcript row anchors
// to when the plan actually changed rather than the debounced flush instant (which can trail the
// coordinator's synthesis prose and sort the row to the bottom).
type FlushPayload struct {
	SessionID string
	Baseline  string
	Latest    string
	Seq       int
	ChangedAt time.Time
}

// FlushFunc receives a coalesced window when it settles.
type FlushFunc func(payload FlushPayload)

// Coalescer debounces per-session update_progress writes so a burst of edits drops a single change
// row into chat. Each session keeps a window seeded with the pre-write content of its first write;
// later writes only advance the latest content and reset the timer.
type Coalescer struct {
	mu      sync.Mutex
	window  time.Duration
	emit    FlushFunc
	now     func() time.Time
	pending map[string]*coalesceWindow
	seq     map[string]int
}

type coalesceWindow struct {
	baseline  string
	latest    string
	changedAt time.Time
	timer     *time.Timer
}

// NewCoalescer uses DefaultCoalesceWindow when window is not positive.
func NewCoalescer(window time.Duration, emit FlushFunc) *Coalescer {
	if window <= 0 {
		window = DefaultCoalesceWindow
	}
	return &Coalescer{
		window:  window,
		emit:    emit,
		now:     func() time.Time { return time.Now().UTC() },
		pending: make(map[string]*coalesceWindow),
		seq:     make(map[string]int),
	}
}

// Record folds one update_progress write into the session's open window, seeding the baseline on the
// first write and (re)arming the flush timer.
func (c *Coalescer) Record(sessionID, prevContent, nextContent string) {
	key := strings.TrimSpace(sessionID)
	if key == "" || c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.pending[key]
	if w == nil {
		w = &coalesceWindow{baseline: prevContent}
		c.pending[key] = w
	}
	w.latest = nextContent
	w.changedAt = c.now()
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
	if key == "" {
		return
	}
	c.mu.Lock()
	w := c.pending[key]
	if w == nil {
		c.mu.Unlock()
		return
	}
	delete(c.pending, key)
	if w.timer != nil {
		w.timer.Stop()
	}
	c.seq[key]++
	payload := FlushPayload{
		SessionID: key,
		Baseline:  w.baseline,
		Latest:    w.latest,
		Seq:       c.seq[key],
		ChangedAt: w.changedAt,
	}
	emit := c.emit
	c.mu.Unlock()

	if emit != nil {
		emit(payload)
	}
}
