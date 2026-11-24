package repochange

import (
	"context"
	"sync"
	"time"
)

// DefaultDebounce is the coalesce window for WorktreeChanged bursts.
const DefaultDebounce = 75 * time.Millisecond

// debouncer coalesces WorktreeChanged Notify calls per project dir.
type debouncer struct {
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

// WaitQuiet is the instance form of the package-level WaitQuiet.
func (d *debouncer) WaitQuiet(ctx context.Context, dir string, grace, maxWait time.Duration) {
	if d == nil || maxWait <= 0 {
		return
	}
	key := physicalDir(dir)
	if key == "" {
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
			pause = d.currentDelay() / 4
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
	timer      *time.Timer
	root       string
	projectDir string
	done       chan struct{}
	changes    map[string]WorktreeChangeKind
	source     Source
	firstEpoch Epoch
	epoch      Epoch
}

// newDebouncer returns a coalescing Notify helper. delay ≤ 0 uses DefaultDebounce.
func newDebouncer(delay time.Duration) *debouncer {
	if delay <= 0 {
		delay = DefaultDebounce
	}
	return &debouncer{delay: delay, pending: map[string]*debounceEntry{}, active: map[*debounceEntry]struct{}{}, flushed: map[string]time.Time{}}
}

func (d *debouncer) currentDelay() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.delay
}

// UseDebounce flushes pending changes before swapping the process coalesce window.
func UseDebounce(ctx context.Context, delay time.Duration) (restore func()) {
	flushCtx := context.WithoutCancel(ctx)
	set := func(next time.Duration) {
		d := processWatchers().debounce
		d.flush(flushCtx)
		d.mu.Lock()
		d.delay = next
		d.mu.Unlock()
	}
	previous := processWatchers().debounce.currentDelay()
	set(delay)
	return func() { set(previous) }
}

// FlushDir delivers any pending debounced burst for dir and waits for in-flight deliveries on dir.
func FlushDir(ctx context.Context, dir string) {
	processWatchers().debounce.flushDir(ctx, dir)
}

func (d *debouncer) flushDir(ctx context.Context, dir string) {
	if d == nil {
		return
	}
	key := physicalDir(dir)
	if key == "" {
		return
	}
	flushCtx := context.WithoutCancel(ctx)
	for range flushPasses {
		d.mu.Lock()
		ent := d.pending[key]
		if ent != nil && ent.timer != nil {
			ent.timer.Stop()
		}
		var inflight []*debounceEntry
		for active := range d.active {
			if active.root == key && active != ent {
				inflight = append(inflight, active)
			}
		}
		d.mu.Unlock()
		if ent != nil {
			d.deliver(flushCtx, key, ent)
		}
		for _, active := range inflight {
			select {
			case <-active.done:
			case <-ctx.Done():
				return
			}
		}
		if ent == nil && len(inflight) == 0 {
			return
		}
	}
}

// flushPasses bounds work from continuous producers.
const flushPasses = 8

func (d *debouncer) flush(ctx context.Context) {
	if d == nil {
		return
	}
	for range flushPasses {
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

// NotifyWorktreeDebounced merges paths for dir and fires one WorktreeChanged after delay.
func NotifyWorktreeDebounced(ctx context.Context, dir string, paths []string, source Source) {
	processWatchers().debounce.NotifyWorktree(ctx, dir, paths, source)
}

// NotifyWorktreeChangesDebounced merges structured path changes for dir and fires one WorktreeChanged after delay.
func NotifyWorktreeChangesDebounced(ctx context.Context, dir string, changes []WorktreeChange, source Source) {
	processWatchers().debounce.NotifyWorktreeChanges(ctx, dir, changes, source)
}

// NotifyWorktree schedules a coalesced WorktreeChanged for dir.
func (d *debouncer) NotifyWorktree(ctx context.Context, dir string, paths []string, source Source) {
	changes := make([]WorktreeChange, 0, len(paths))
	for _, path := range paths {
		changes = append(changes, WorktreeChange{Path: path, Kind: WorktreeChangeUnknown})
	}
	d.NotifyWorktreeChanges(ctx, dir, changes, source)
}

// NotifyWorktreeChanges schedules a coalesced WorktreeChanged with structured path facts.
func (d *debouncer) NotifyWorktreeChanges(ctx context.Context, dir string, changes []WorktreeChange, source Source) <-chan struct{} {
	if d == nil {
		Notify(ctx, Event{ProjectDir: dir, Kind: WorktreeChanged, Changes: changes, Source: source})
		return nil
	}
	projectDir := canonicalDir(dir)
	key := physicalDir(dir)
	if projectDir == "" || key == "" {
		return nil
	}
	if source == "" {
		source = SourceMutation
	}
	normalized := normalizeDebouncedChanges(projectDir, key, changes, source)
	if len(normalized) == 0 {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	ent := d.pending[key]
	if ent == nil {
		first := Advance(key)
		ent = &debounceEntry{root: key, projectDir: projectDir, done: make(chan struct{}), changes: map[string]WorktreeChangeKind{}, source: source, firstEpoch: first, epoch: first}
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
	for _, change := range normalized {
		path := change.Path
		if path == "." && change.Kind == WorktreeChangeResync {
			clear(ent.changes)
			ent.changes["."] = WorktreeChangeResync
			return ent.done
		}
		if len(ent.changes) >= maxDebouncePaths {
			// A burst wider than any consumer reconciles path by path is the
			// whole tree; naming the root says so in one entry.
			clear(ent.changes)
			ent.changes["."] = WorktreeChangeResync
			return ent.done
		}
		if ent.changes["."] == WorktreeChangeResync {
			return ent.done
		}
		if previous, found := ent.changes[path]; found {
			ent.changes[path] = mergeWorktreeChangeKind(previous, change.Kind)
		} else {
			ent.changes[path] = change.Kind
		}
	}
	return ent.done
}

// maxDebouncePaths bounds one flush. Past it the flush names the root, which
// every consumer reads as a whole-tree change.
const maxDebouncePaths = 65536

func (d *debouncer) deliver(ctx context.Context, key string, ent *debounceEntry) {
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
		ProjectDir: ent.projectDir, Kind: WorktreeChanged, Changes: changes,
		Source: ent.source, FirstWorktreeEpoch: ent.firstEpoch.Value,
		WorktreeEpoch: ent.epoch.Value, EpochBootID: ent.epoch.BootID,
	})
}

// retire seals new bursts and settles every accepted delivery.
func (d *debouncer) retire(ctx context.Context) error {
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
