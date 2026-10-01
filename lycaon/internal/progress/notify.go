package progress

import (
	"context"
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

var (
	writeMu        sync.RWMutex
	writeObservers []WriteObserver
)

// RegisterWriteObserver attaches a post-write hook.
func RegisterWriteObserver(obs WriteObserver) {
	if obs == nil {
		return
	}
	writeMu.Lock()
	writeObservers = append(writeObservers, obs)
	writeMu.Unlock()
}

// NotifyWriteObservers invokes registered observers after a write lands.
func NotifyWriteObservers(ctx context.Context, evt WriteEvent) {
	writeMu.RLock()
	obs := append([]WriteObserver(nil), writeObservers...)
	writeMu.RUnlock()
	for _, fn := range obs {
		fn(ctx, evt)
	}
}
