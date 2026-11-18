package webresearch

import (
	"context"
	"sync"
	"sync/atomic"
)

// Discovery coordinates interactive searches and background warming.
type Discovery struct {
	slots     chan struct{}
	active    atomic.Int64
	seedMu    sync.Mutex
	seedSlots map[string]chan struct{}
	plans     *ttlCache[seedPlan]
	memoryMu  sync.Mutex
	seen      map[string]*seenScope
}

func NewDiscovery() *Discovery {
	return &Discovery{
		slots:     make(chan struct{}, directDiscoverMaxParallel),
		seedSlots: make(map[string]chan struct{}),
		plans:     newTTLCache[seedPlan](seedPlanCacheTTL, seedPlanCacheMax),
		seen:      make(map[string]*seenScope),
	}
}

func (d *Discovery) interactiveSearchActive() bool { return d.active.Load() > 0 }

func acquireSlot(ctx context.Context, sem chan struct{}) (func(), error) {
	select {
	case sem <- struct{}{}:
		return sync.OnceFunc(func() { <-sem }), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (d *Discovery) acquire(ctx context.Context) (func(), error) { return acquireSlot(ctx, d.slots) }

func (d *Discovery) tryAcquire() (func(), bool) {
	select {
	case d.slots <- struct{}{}:
		return sync.OnceFunc(func() { <-d.slots }), true
	default:
		return nil, false
	}
}

func (d *Discovery) acquireSeed(ctx context.Context, providerID string, shared bool) (func(), error) {
	key := seedSemKey(providerID, shared)
	d.seedMu.Lock()
	sem := d.seedSlots[key]
	if sem == nil {
		sem = make(chan struct{}, seedCallParallelLimit(shared))
		d.seedSlots[key] = sem
	}
	d.seedMu.Unlock()
	return acquireSlot(ctx, sem)
}
