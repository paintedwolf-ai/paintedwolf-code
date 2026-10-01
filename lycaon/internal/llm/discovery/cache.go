package discovery

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
)

// CacheTTL bounds reuse of a completed provider listing.
const CacheTTL = 30 * time.Second

// Cache shares refresh work and retains the last usable model catalog.
type Cache struct {
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]discoveryCacheEntry
	flights map[string]*discoveryFlight
	closed  bool
	workers sync.WaitGroup
}

type discoveryCacheEntry struct {
	models  []modelinfo.Entry
	fetched time.Time
	err     error
}

type discoveryFlight struct {
	done   chan struct{}
	cancel context.CancelFunc
	models []modelinfo.Entry
	err    error
}

// NewCache uses the wall clock when now is nil.
func NewCache(now func() time.Time) *Cache {
	if now == nil {
		now = time.Now
	}
	return &Cache{now: now, entries: make(map[string]discoveryCacheEntry), flights: make(map[string]*discoveryFlight)}
}

// Snapshot retains the last usable catalog while a stale entry is refreshed.
func (c *Cache) Snapshot(key string) (models []modelinfo.Entry, present, fresh bool, lastErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false, false, nil
	}
	return modelinfo.CloneEntries(entry.models), true, c.now().Sub(entry.fetched) <= CacheTTL, entry.err
}

// Get returns the fresh catalog for key, or the error its last fetch recorded.
func (c *Cache) Get(key string) (models []modelinfo.Entry, fresh bool, lastErr error) {
	models, _, fresh, lastErr = c.Snapshot(key)
	if lastErr != nil {
		models = nil
	}
	return models, fresh, lastErr
}

func (c *Cache) Put(key string, models []modelinfo.Entry, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putLocked(key, models, err)
}

func (c *Cache) putLocked(key string, models []modelinfo.Entry, err error) {
	if c.closed {
		return
	}
	if err != nil {
		models = c.entries[key].models
	}
	c.entries[key] = discoveryCacheEntry{models: modelinfo.CloneEntries(models), fetched: c.now(), err: err}
}

// Load joins one refresh; canceling a reader does not cancel other readers.
func (c *Cache) Load(ctx context.Context, key string, fetch func(context.Context) ([]modelinfo.Entry, error)) ([]modelinfo.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	flight := c.start(ctx, key, fetch)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-flight.done:
		return modelinfo.CloneEntries(flight.models), flight.err
	}
}

// Refresh starts at most one bounded request and returns immediately.
func (c *Cache) Refresh(ctx context.Context, key string, fetch func(context.Context) ([]modelinfo.Entry, error)) {
	if ctx.Err() == nil {
		c.start(ctx, key, fetch)
	}
}

func (c *Cache) start(ctx context.Context, key string, fetch func(context.Context) ([]modelinfo.Entry, error)) *discoveryFlight {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return completedDiscovery(nil, context.Canceled)
	}
	if entry, ok := c.entries[key]; ok && c.now().Sub(entry.fetched) <= CacheTTL {
		if entry.err != nil {
			return completedDiscovery(nil, entry.err)
		}
		return completedDiscovery(entry.models, nil)
	}
	if flight := c.flights[key]; flight != nil {
		return flight
	}
	// Discovery has provider-specific network deadlines. The cache owns its
	// lifetime so a prompt's cancellation cannot strand another caller's refresh.
	workCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	flight := &discoveryFlight{done: make(chan struct{}), cancel: cancel}
	c.flights[key] = flight
	c.workers.Add(1)
	go c.fetch(workCtx, key, flight, fetch)
	return flight
}

func completedDiscovery(models []modelinfo.Entry, err error) *discoveryFlight {
	flight := &discoveryFlight{done: make(chan struct{}), models: modelinfo.CloneEntries(models), err: err}
	close(flight.done)
	return flight
}

func (c *Cache) fetch(ctx context.Context, key string, flight *discoveryFlight, fetch func(context.Context) ([]modelinfo.Entry, error)) {
	defer c.workers.Done()
	defer flight.cancel()
	models, err := fetch(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.flights[key] == flight {
		delete(c.flights, key)
		if ctx.Err() == nil {
			c.putLocked(key, models, err)
		}
	}
	flight.models, flight.err = modelinfo.CloneEntries(models), err
	if ctx.Err() != nil {
		flight.models, flight.err = nil, ctx.Err()
	}
	close(flight.done)
}

func (c *Cache) Invalidate(providerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := providerID + "\x00"
	for key := range c.entries {
		if key == providerID || strings.HasPrefix(key, prefix) {
			delete(c.entries, key)
		}
	}
	for key, flight := range c.flights {
		if key == providerID || strings.HasPrefix(key, prefix) {
			flight.cancel()
			delete(c.flights, key)
		}
	}
}

func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.entries)
	for _, flight := range c.flights {
		flight.cancel()
	}
	clear(c.flights)
}

func (c *Cache) Close(ctx context.Context) error {
	c.mu.Lock()
	c.closed = true
	for _, flight := range c.flights {
		flight.cancel()
	}
	c.mu.Unlock()
	done := make(chan struct{})
	go func() { c.workers.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
