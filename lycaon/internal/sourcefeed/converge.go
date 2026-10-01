package sourcefeed

import (
	"context"
	"sort"
	"sync"
	"time"
)

const (
	externalChangeQuietWindow = 250 * time.Millisecond
	externalChangeMaxDelay    = 5 * time.Second
	// A held window keeps collecting past the maximum delay, up to this long.
	externalChangeHoldCeiling = 2 * time.Minute
)

type pendingExternalChange struct {
	root RootSpec
	rel  string
}

type pendingExternalBatch struct {
	changes []pendingExternalChange
	resync  bool
	// headMoved records that a git ref surface moved inside this window, so a
	// flush reconciles (and republishes git state) even with no file changes.
	headMoved bool
}

// changeConverger batches path states until quiet or the maximum delay.
// Active holds extend the window up to holdCeiling.
type changeConverger struct {
	mu          sync.Mutex
	quiet       time.Duration
	maxDelay    time.Duration
	hold        func() bool
	holdCeiling time.Duration
	pending     map[string]pendingExternalChange
	first       time.Time
	timer       *time.Timer
	generation  uint64
	overflow    bool
	headMoved   bool
	closed      bool
	flush       func(context.Context, pendingExternalBatch)
}

func newChangeConverger(quiet, maxDelay time.Duration, flush func(context.Context, pendingExternalBatch)) *changeConverger {
	if quiet <= 0 {
		quiet = externalChangeQuietWindow
	}
	if maxDelay <= 0 {
		maxDelay = externalChangeMaxDelay
	}
	maxDelay = max(maxDelay, quiet)
	return &changeConverger{
		quiet: quiet, maxDelay: maxDelay, holdCeiling: externalChangeHoldCeiling,
		pending: make(map[string]pendingExternalChange), flush: flush,
	}
}

// holdWhile keeps a window open while the predicate holds, up to ceiling.
func (c *changeConverger) holdWhile(hold func() bool, ceiling time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hold = hold
	c.holdCeiling = max(ceiling, c.maxDelay)
}

func (c *changeConverger) queue(ctx context.Context, root RootSpec, rel string) {
	if c == nil || rel == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	// Past the per-window cap the flush is one invalidation, so individual
	// paths stop being worth holding.
	if c.overflow {
		c.armLocked(ctx)
		return
	}
	key := root.ID + "\x00" + rel
	if _, exists := c.pending[key]; exists || len(c.pending) < maxChangesPerEvent {
		c.pending[key] = pendingExternalChange{root: root, rel: rel}
	} else {
		c.overflow = true
		clear(c.pending)
	}
	c.armLocked(ctx)
}

// queueHeadMoved schedules a flush for a git ref movement with no file paths,
// coalescing into the same window as any accompanying worktree changes so a
// checkout reconciles once, not twice.
func (c *changeConverger) queueHeadMoved(ctx context.Context) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.headMoved = true
	c.armLocked(ctx)
}

// queueResync records that the watcher could not identify every changed path.
// The next flush reconciles the source ledger and publishes a resync event.
func (c *changeConverger) queueResync(ctx context.Context) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.overflow = true
	c.armLocked(ctx)
}

func (c *changeConverger) armLocked(ctx context.Context) {
	now := time.Now()
	if c.first.IsZero() {
		c.first = now
	}
	delay := c.quiet
	if remaining := c.maxDelay - now.Sub(c.first); remaining < delay {
		delay = remaining
	}
	if delay < 0 {
		delay = 0
	}
	if c.timer != nil {
		c.timer.Stop()
	}
	c.generation++
	generation := c.generation
	flushCtx := context.WithoutCancel(ctx)
	c.timer = time.AfterFunc(delay, func() { c.fire(flushCtx, generation) })
}

func (c *changeConverger) fire(ctx context.Context, generation uint64) {
	c.mu.Lock()
	if c.closed || generation != c.generation {
		c.mu.Unlock()
		return
	}
	if c.hold != nil && time.Since(c.first) < c.holdCeiling && c.hold() {
		c.generation++
		next := c.generation
		c.timer = time.AfterFunc(c.quiet, func() { c.fire(ctx, next) })
		c.mu.Unlock()
		return
	}
	changes := c.takeLocked()
	c.mu.Unlock()
	if (len(changes.changes) > 0 || changes.resync || changes.headMoved) && c.flush != nil {
		c.flush(ctx, changes)
	}
}

func (c *changeConverger) close(ctx context.Context) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.closed = true
	c.generation++
	changes := c.takeLocked()
	c.mu.Unlock()
	if (len(changes.changes) > 0 || changes.resync || changes.headMoved) && c.flush != nil {
		c.flush(ctx, changes)
	}
}

func (c *changeConverger) takeLocked() pendingExternalBatch {
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	out := make([]pendingExternalChange, 0, len(c.pending))
	for _, change := range c.pending {
		out = append(out, change)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].root.ID != out[j].root.ID {
			return out[i].root.ID < out[j].root.ID
		}
		return out[i].rel < out[j].rel
	})
	clear(c.pending)
	c.first = time.Time{}
	batch := pendingExternalBatch{changes: out, resync: c.overflow, headMoved: c.headMoved}
	c.overflow = false
	c.headMoved = false
	return batch
}
