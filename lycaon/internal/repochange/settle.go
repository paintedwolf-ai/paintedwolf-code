package repochange

import (
	"context"
	"path/filepath"
	"time"
)

// SettleRoot flushes the root's platform watcher barrier and waits until the
// debouncer reaches quiet for grace, bounded by maxWait. It returns false when
// the barrier or quiet wait did not settle before maxWait or ctx cancellation.
func SettleRoot(ctx context.Context, root string, grace, maxWait time.Duration) bool {
	return processWatchers().Settle(ctx, root, grace, maxWait)
}

// Settle flushes any live watcher for root into the debouncer, then waits for
// the debouncer to reach quiet.
func (r *watcherRegistry) Settle(ctx context.Context, root string, grace, maxWait time.Duration) bool {
	if r == nil {
		return true
	}
	if maxWait <= 0 {
		return false
	}
	key, err := filepath.Abs(root)
	if err != nil || key == "" {
		return false
	}
	r.mu.Lock()
	w := r.byDir[key]
	r.mu.Unlock()
	if w != nil {
		flushCtx, cancel := context.WithTimeout(ctx, maxWait)
		ok := w.flush(flushCtx)
		cancel()
		if !ok {
			return false
		}
	}
	return r.debounce.WaitQuiet(ctx, key, grace, maxWait)
}

// flush drains events up to "now" from the platform watcher through the
// worktree loop into the debouncer.
func (w *worktreeWatcher) flush(ctx context.Context) bool {
	if w == nil {
		return true
	}
	if flusher, ok := w.watcher.(flushingPlatformWatcher); ok {
		if !flusher.Flush(ctx) {
			return false
		}
	}
	ack := make(chan struct{})
	select {
	case w.syncCh <- ack:
	case <-w.done:
		return false
	case <-ctx.Done():
		return false
	}
	select {
	case <-ack:
		return true
	case <-w.done:
		return false
	case <-ctx.Done():
		return false
	}
}

// drainPending processes any events already buffered on the platform watcher
// channels before acknowledging a flush barrier.
func (w *worktreeWatcher) drainPending(ctx context.Context) bool {
	for {
		select {
		case ev, ok := <-w.watcher.EventChannel():
			if !ok {
				return false
			}
			w.handleWatcherEvent(ctx, ev)
		case _, ok := <-w.watcher.ResyncChannel():
			if !ok {
				return false
			}
			w.debounce.NotifyWorktreeChanges(ctx, w.root, []WorktreeChange{{Path: ".", Kind: WorktreeChangeResync}}, SourceWatcher)
			w.refs.reconcile(ctx, w.addMeasuredDir)
		default:
			return true
		}
	}
}
