package progress

import (
	"context"
	"slices"
	"sync"
)

// WriteEvent is emitted after an update_progress write lands. Prev is the checklist content
// immediately before the write, captured so observers can diff prev → current.
type WriteEvent struct {
	SessionID string
	Prev      string
}

// WriteObserver runs after a successful progress write (revision bump, SSE, etc.).
type WriteObserver func(ctx context.Context, evt WriteEvent)

type writeObserverEntry struct {
	id  uint64
	obs WriteObserver
}

var (
	writeMu        sync.RWMutex
	writeObservers []writeObserverEntry
	writeSeq       uint64
)

// RegisterWriteObserver attaches a post-write hook and returns its release.
// The registry is process-wide: a host that never releases its hook keeps its
// whole object graph reachable after shutdown.
func RegisterWriteObserver(obs WriteObserver) func() {
	if obs == nil {
		return func() {}
	}
	writeMu.Lock()
	writeSeq++
	id := writeSeq
	writeObservers = append(writeObservers, writeObserverEntry{id: id, obs: obs})
	writeMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			writeMu.Lock()
			writeObservers = slices.DeleteFunc(writeObservers, func(e writeObserverEntry) bool { return e.id == id })
			writeMu.Unlock()
		})
	}
}

// NotifyWriteObservers invokes registered observers after a write lands.
func NotifyWriteObservers(ctx context.Context, evt WriteEvent) {
	writeMu.RLock()
	entries := slices.Clone(writeObservers)
	writeMu.RUnlock()
	for _, e := range entries {
		e.obs(ctx, evt)
	}
}
