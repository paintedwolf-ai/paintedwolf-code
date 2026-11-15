package briefing

import (
	"context"
	"sync"
)

// SnapshotHandler receives one profile's committed snapshots.
type SnapshotHandler func(ctx context.Context, projectID string, snapshot Snapshot)

// Router delivers each snapshot to its profile's handler and to every
// handler registered for all profiles.
type Router struct {
	mu       sync.RWMutex
	profiles map[string][]SnapshotHandler
	all      []SnapshotHandler
}

func NewRouter() *Router { return &Router{profiles: map[string][]SnapshotHandler{}} }

// Handle registers a handler for one profile.
func (r *Router) Handle(profileID string, handler SnapshotHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.profiles[profileID] = append(r.profiles[profileID], handler)
}

// HandleAll registers a handler for every profile.
func (r *Router) HandleAll(handler SnapshotHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.all = append(r.all, handler)
}

func (r *Router) PublishSnapshot(ctx context.Context, projectID string, snapshot Snapshot) {
	r.mu.RLock()
	handlers := append(append([]SnapshotHandler(nil), r.profiles[snapshot.ProfileID]...), r.all...)
	r.mu.RUnlock()
	for _, handler := range handlers {
		handler(ctx, projectID, snapshot)
	}
}
