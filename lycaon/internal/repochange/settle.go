package repochange

import (
	"context"
	"path/filepath"
	"sync"
	"time"
)

var (
	sentinelDeviceMu    sync.RWMutex
	sentinelDeviceCheck func(a, b string) bool
)

// UseSentinelDeviceCheck overrides the sentinel same-volume admission check
// and returns a restore function. Passing nil restores the default OS check.
func UseSentinelDeviceCheck(fn func(a, b string) bool) (restore func()) {
	sentinelDeviceMu.Lock()
	prev := sentinelDeviceCheck
	sentinelDeviceCheck = fn
	sentinelDeviceMu.Unlock()
	return func() {
		sentinelDeviceMu.Lock()
		sentinelDeviceCheck = prev
		sentinelDeviceMu.Unlock()
	}
}

func checkSentinelDevice(a, b string, fallback func(a, b string) bool) bool {
	sentinelDeviceMu.RLock()
	fn := sentinelDeviceCheck
	sentinelDeviceMu.RUnlock()
	if fn != nil {
		return fn(a, b)
	}
	if fallback != nil {
		return fallback(a, b)
	}
	return true
}

// SettleRoot flushes the root's platform watcher barrier and waits until the
// debouncer reaches quiet for grace, bounded by maxWait. It returns true only
// when the barrier proved delivery and the debouncer reached quiet.
func SettleRoot(ctx context.Context, root string, grace, maxWait time.Duration) bool {
	return SettleRootStatus(ctx, root, grace, maxWait) == SettleQuiet
}

// SettleRootStatus flushes the root's platform watcher barrier and waits until
// the debouncer reaches quiet, distinguishing SettleQuiet, SettleUnproven (no
// sentinel barrier available), and SettleUnsettled (timed out or canceled).
func SettleRootStatus(ctx context.Context, root string, grace, maxWait time.Duration) SettleStatus {
	return processWatchers().SettleStatus(ctx, root, grace, maxWait)
}

// Settle flushes any live watcher for root into the debouncer, then waits for
// the debouncer to reach quiet.
func (r *watcherRegistry) Settle(ctx context.Context, root string, grace, maxWait time.Duration) bool {
	return r.SettleStatus(ctx, root, grace, maxWait) == SettleQuiet
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

// flush drains events up to "now" from the platform watcher through the
// worktree loop into the debouncer.
func (w *worktreeWatcher) flush(ctx context.Context) bool {
	return w.flushStatus(ctx) == SettleQuiet
}

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
