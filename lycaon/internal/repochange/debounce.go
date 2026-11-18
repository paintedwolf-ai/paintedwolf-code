package repochange

import (
	"context"
	"path/filepath"
	"sync"
	"time"
)

// DefaultDebounce is the coalesce window for WorktreeChanged bursts.
const DefaultDebounce = 75 * time.Millisecond

// Debouncer coalesces WorktreeChanged Notify calls per project dir.
type Debouncer struct {
	delay time.Duration

	mu      sync.Mutex
	pending map[string]*debounceEntry
	active  map[*debounceEntry]struct{}
	closed  bool
	// flushed records when each dir last delivered, so a consumer that trusts
	// delivered events can wait for the pipeline to drain first.
	flushed map[string]time.Time
}

// WaitQuiet blocks until dir has had no pending burst for grace, or until
// maxWait elapses. A consumer that builds from delivered events rather than
// from disk calls it first, because a write that just landed reaches the
// platform stream and the debouncer only after a delay.
func WaitQuiet(ctx context.Context, dir string, grace, maxWait time.Duration) {
	processWatchers().debounce.WaitQuiet(ctx, dir, grace, maxWait)
}

// WaitQuiet is the Debouncer form of the package-level WaitQuiet.
func (d *Debouncer) WaitQuiet(ctx context.Context, dir string, grace, maxWait time.Duration) {
	if d == nil || maxWait <= 0 {
		return
	}
	key, err := filepath.Abs(dir)
	if err != nil || key == "" {
		return
	}
	started := time.Now()
	deadline := started.Add(maxWait)
	for {
		d.mu.Lock()
		_, busy := d.pending[key]
		for entry := range d.active {
			busy = busy || entry.root == key
		}
		last := d.flushed[key]
		d.mu.Unlock()
		if last.Before(started) {
			// A write that landed just before the call is still in flight
			// on the platform stream; the grace runs from the call itself.
			last = started
		}
		quietFor := time.Since(last)
		if !busy && quietFor >= grace {
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return
		}
		pause := grace - quietFor
		if busy || pause <= 0 {
			pause = d.delay / 4
		}
		if pause > remaining {
			pause = remaining
		}
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

type debounceEntry struct {
	timer   *time.Timer
	root    string
	done    chan struct{}
	changes map[string]WorktreeChangeKind
	source  Source
	epoch   Epoch
}

// NewDebouncer returns a coalescing Notify helper. delay ≤ 0 uses DefaultDebounce.
func NewDebouncer(delay time.Duration) *Debouncer {
	if delay <= 0 {
		delay = DefaultDebounce
	}
	return &Debouncer{delay: delay, pending: map[string]*debounceEntry{}, active: map[*debounceEntry]struct{}{}, flushed: map[string]time.Time{}}
}

// NotifyWorktreeDebounced merges paths for dir and fires one WorktreeChanged after delay.
func NotifyWorktreeDebounced(ctx context.Context, dir string, paths []string, source Source) {
	processWatchers().debounce.NotifyWorktree(ctx, dir, paths, source)
}

// NotifyWorktree schedules a coalesced WorktreeChanged for dir.
func (d *Debouncer) NotifyWorktree(ctx context.Context, dir string, paths []string, source Source) {
	changes := make([]WorktreeChange, 0, len(paths))
	for _, path := range paths {
		changes = append(changes, WorktreeChange{Path: path, Kind: WorktreeChangeUnknown})
	}
	d.NotifyWorktreeChanges(ctx, dir, changes, source)
}

func NotifyWorktreeChangesDebounced(ctx context.Context, dir string, changes []WorktreeChange, source Source) {
	processWatchers().debounce.NotifyWorktreeChanges(ctx, dir, changes, source)
}

// NotifyWorktreeChanges schedules a coalesced WorktreeChanged with structured path facts.
func (d *Debouncer) NotifyWorktreeChanges(ctx context.Context, dir string, changes []WorktreeChange, source Source) {
	if d == nil {
		Notify(ctx, Event{ProjectDir: dir, Kind: WorktreeChanged, Changes: changes, Source: source})
		return
	}
	key, err := filepath.Abs(dir)
	if err != nil || key == "" {
		return
	}
	if source == "" {
		source = SourceMutation
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	ent := d.pending[key]
	if ent == nil {
		ent = &debounceEntry{root: key, done: make(chan struct{}), changes: map[string]WorktreeChangeKind{}, source: source, epoch: Advance(key)}
		d.pending[key] = ent
		d.active[ent] = struct{}{}
		// Preserve context values after the mutating call returns.
		flushCtx := context.WithoutCancel(ctx)
		ent.timer = time.AfterFunc(d.delay, func() {
			d.deliver(flushCtx, key, ent)
		})
	}
	if ent != nil && len(ent.changes) > 0 {
		ent.epoch = Advance(key)
	}
	// Prefer mutation over watcher when a burst mixes sources.
	if source == SourceMutation || ent.source == "" {
		ent.source = source
	}
	for _, change := range changes {
		path := filepath.ToSlash(change.Path)
		if path == "" {
			continue
		}
		if len(ent.changes) >= maxDebouncePaths {
			// A burst wider than any consumer reconciles path by path is the
			// whole tree; naming the root says so in one entry.
			clear(ent.changes)
			ent.changes["."] = WorktreeChangeResync
			return
		}
		if ent.changes["."] == WorktreeChangeResync {
			return
		}
		if previous, found := ent.changes[path]; found {
			ent.changes[path] = mergeWorktreeChangeKind(previous, change.Kind)
		} else {
			ent.changes[path] = change.Kind
		}
	}
}

// maxDebouncePaths bounds one flush. Past it the flush names the root, which
// every consumer reads as a whole-tree change.
const maxDebouncePaths = 65536

func (d *Debouncer) deliver(ctx context.Context, key string, ent *debounceEntry) {
	d.mu.Lock()
	if d.pending[key] != ent {
		d.mu.Unlock()
		return
	}
	delete(d.pending, key)
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.active, ent)
		d.flushed[key] = time.Now()
		close(ent.done)
		d.mu.Unlock()
	}()
	changes := make([]WorktreeChange, 0, len(ent.changes))
	for path, kind := range ent.changes {
		changes = append(changes, WorktreeChange{Path: path, Kind: kind})
	}
	Notify(ctx, Event{
		ProjectDir: key, Kind: WorktreeChanged, Changes: changes,
		Source: ent.source, WorktreeEpoch: ent.epoch.Value, EpochBootID: ent.epoch.BootID,
	})
}

// retire seals new bursts and settles every accepted delivery.
func (d *Debouncer) retire(ctx context.Context) error {
	d.mu.Lock()
	d.closed = true
	entries := make([]*debounceEntry, 0, len(d.active))
	for ent := range d.active {
		entries = append(entries, ent)
		if ent.timer.Stop() {
			go d.deliver(context.WithoutCancel(ctx), ent.root, ent)
		}
	}
	d.mu.Unlock()
	for _, ent := range entries {
		select {
		case <-ent.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// flushForTestPasses bounds work from continuous producers.
const flushForTestPasses = 8

// FlushForTest synchronously drains pending timers.
func (d *Debouncer) FlushForTest(ctx context.Context) {
	if d == nil {
		return
	}
	for range flushForTestPasses {
		d.mu.Lock()
		keys := make([]string, 0, len(d.pending))
		for k, ent := range d.pending {
			if ent.timer != nil {
				ent.timer.Stop()
			}
			keys = append(keys, k)
		}
		d.mu.Unlock()
		if len(keys) == 0 {
			return
		}
		for _, k := range keys {
			d.mu.Lock()
			ent := d.pending[k]
			d.mu.Unlock()
			if ent != nil {
				d.deliver(ctx, k, ent)
			}
		}
	}
}

// ResetDebouncerForTest drains the global debouncer.
func ResetDebouncerForTest(ctx context.Context) {
	processWatchers().debounce.FlushForTest(ctx)
}
