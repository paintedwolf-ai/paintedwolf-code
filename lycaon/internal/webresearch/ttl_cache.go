package webresearch

import (
	"sync"
	"time"
)

// ttlCache is the shared shape of the package's in-process crawl caches: TTL
// expiry on read, oldest-entry eviction at capacity. Not for stateful
// registries (polite gates, seen-URL scopes) — those mutate entries in place
// and keep their own eviction.
type ttlCache[V any] struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	entries map[string]ttlEntry[V]
}

type ttlEntry[V any] struct {
	value V
	at    time.Time
}

func newTTLCache[V any](ttl time.Duration, max int) *ttlCache[V] {
	return &ttlCache[V]{ttl: ttl, max: max, entries: make(map[string]ttlEntry[V])}
}

func (c *ttlCache[V]) get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Since(e.at) > c.ttl {
		var zero V
		return zero, false
	}
	return e.value, true
}

func (c *ttlCache[V]) put(key string, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.max {
		oldestKey, oldestAt := "", time.Time{}
		for k, e := range c.entries {
			if oldestKey == "" || e.at.Before(oldestAt) {
				oldestKey, oldestAt = k, e.at
			}
		}
		delete(c.entries, oldestKey)
	}
	c.entries[key] = ttlEntry[V]{value: value, at: time.Now()}
}

func (c *ttlCache[V]) reset() {
	c.mu.Lock()
	c.entries = make(map[string]ttlEntry[V])
	c.mu.Unlock()
}
