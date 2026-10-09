package findings

import (
	"context"
	"slices"
	"sync"
)

// AppendEvent is emitted after a record_finding append lands.
type AppendEvent struct {
	SessionID string
}

// AppendObserver runs after a successful findings append.
type AppendObserver func(ctx context.Context, evt AppendEvent)

type appendObserverEntry struct {
	id  uint64
	obs AppendObserver
}

var (
	appendMu        sync.RWMutex
	appendObservers []appendObserverEntry
	appendSeq       uint64
)

// RegisterAppendObserver attaches a post-append hook (revision bump, SSE, etc.)
// and returns its release. The registry is process-wide: a host that never
// releases its hook keeps its whole object graph reachable after shutdown.
func RegisterAppendObserver(obs AppendObserver) func() {
	if obs == nil {
		return func() {}
	}
	appendMu.Lock()
	appendSeq++
	id := appendSeq
	appendObservers = append(appendObservers, appendObserverEntry{id: id, obs: obs})
	appendMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			appendMu.Lock()
			appendObservers = slices.DeleteFunc(appendObservers, func(e appendObserverEntry) bool { return e.id == id })
			appendMu.Unlock()
		})
	}
}

// NotifyAppendObservers invokes registered observers after an append lands.
func NotifyAppendObservers(ctx context.Context, evt AppendEvent) {
	appendMu.RLock()
	entries := slices.Clone(appendObservers)
	appendMu.RUnlock()
	for _, e := range entries {
		e.obs(ctx, evt)
	}
}
