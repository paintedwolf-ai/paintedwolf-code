package repochange

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"time"
)

// SentinelDeviceCheckForTest overrides the sentinel same-volume admission check.
var SentinelDeviceCheckForTest atomic.Pointer[func(a, b string) bool]

func checkSentinelDevice(a, b string, fallback func(a, b string) bool) bool {
	if fn := SentinelDeviceCheckForTest.Load(); fn != nil {
		return (*fn)(a, b)
	}
	if fallback != nil {
		return fallback(a, b)
	}
	return true
}

// SettleRootStatus flushes the root's platform watcher barrier and waits until
// the debouncer reaches quiet, distinguishing SettleQuiet, SettleUnproven (no
// sentinel barrier available), and SettleUnsettled (timed out or canceled).
func SettleRootStatus(ctx context.Context, root string, grace, maxWait time.Duration) SettleStatus {
	return processWatchers().SettleStatus(ctx, root, grace, maxWait)
}

// SettleStatus flushes any live watcher for root into the debouncer, then waits
// for the debouncer to reach quiet.
func (r *watcherRegistry) SettleStatus(ctx context.Context, root string, grace, maxWait time.Duration) SettleStatus {
	if r == nil {
		return SettleQuiet
	}
	if maxWait <= 0 {
		return SettleUnsettled
	}
	key, err := filepath.Abs(root)
	if err != nil || key == "" {
		return SettleUnsettled
	}
	r.mu.Lock()
	w := r.byDir[key]
	r.mu.Unlock()
	if w != nil {
		flushCtx, cancel := context.WithTimeout(ctx, maxWait)
		status := w.flushStatus(flushCtx)
		cancel()
		if status != SettleQuiet {
			return status
		}
	}
	if !r.debounce.WaitQuiet(ctx, key, grace, maxWait) {
		return SettleUnsettled
	}
	return SettleQuiet
}

// flushStatus drains events up to "now" from the platform watcher through the
// worktree loop into the debouncer.
func (w *worktreeWatcher) flushStatus(ctx context.Context) SettleStatus {
	if w == nil {
		return SettleQuiet
	}
	if flusher, ok := w.watcher.(flushingPlatformWatcher); ok {
		if status := flusher.Flush(ctx); status != SettleQuiet {
			return status
		}
	}
	ack := make(chan struct{})
	select {
	case w.syncCh <- ack:
	case <-w.done:
		return SettleUnsettled
	case <-ctx.Done():
		return SettleUnsettled
	}
	select {
	case <-ack:
		return SettleQuiet
	case <-w.done:
		return SettleUnsettled
	case <-ctx.Done():
		return SettleUnsettled
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
