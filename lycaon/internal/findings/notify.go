package findings

import (
	"context"
	"sync"
)

// AppendEvent is emitted after a record_finding append lands.
type AppendEvent struct {
	SessionID string
}

// AppendObserver runs after a successful findings append.
type AppendObserver func(ctx context.Context, evt AppendEvent)

var (
	appendMu        sync.RWMutex
	appendObservers []AppendObserver
)

// RegisterAppendObserver attaches a post-append hook (revision bump, SSE, etc.).
func RegisterAppendObserver(obs AppendObserver) {
	if obs == nil {
		return
	}
	appendMu.Lock()
	appendObservers = append(appendObservers, obs)
	appendMu.Unlock()
}

// NotifyAppendObservers invokes registered observers after an append lands.
func NotifyAppendObservers(ctx context.Context, evt AppendEvent) {
	appendMu.RLock()
	obs := append([]AppendObserver(nil), appendObservers...)
	appendMu.RUnlock()
	for _, fn := range obs {
		fn(ctx, evt)
	}
}
