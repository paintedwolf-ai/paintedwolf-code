package secretmatch

import (
	"context"
	"crypto/sha256"
	"sync"
)

const (
	// maxScreenCacheEntries bounds cached bodies. Only catalog matches are
	// stable enough to cache.
	maxScreenCacheEntries = 2048
	// maxScreenCacheMatches bounds retained spans.
	maxScreenCacheMatches = 1 << 16
)

// screenCacheKey is the content digest of a screened body.
type screenCacheKey [sha256.Size]byte

// screenCache stores catalog spans by content digest.
type screenCache struct {
	mu      sync.Mutex
	entries map[screenCacheKey][]Match
	order   []screenCacheKey
	matches int
}

func newScreenCache() *screenCache {
	return &screenCache{entries: make(map[screenCacheKey][]Match, maxScreenCacheEntries)}
}

// get returns a detached span slice.
func (c *screenCache) get(key screenCacheKey) ([]Match, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	hits, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	return append([]Match(nil), hits...), true
}

// put remembers the catalog-pass spans for one body.
func (c *screenCache) put(key screenCacheKey, hits []Match) {
	if c == nil || len(hits) > maxScreenCacheMatches {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; ok {
		return
	}
	for (len(c.order) >= maxScreenCacheEntries || c.matches+len(hits) > maxScreenCacheMatches) && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		c.matches -= len(c.entries[oldest])
		delete(c.entries, oldest)
	}
	c.entries[key] = append([]Match(nil), hits...)
	c.order = append(c.order, key)
	c.matches += len(hits)
}

// screenCacheKeyFor retains only a body digest.
func screenCacheKeyFor(s string) screenCacheKey {
	return sha256.Sum256([]byte(s))
}

// catalogMatches returns cached or fresh catalog spans.
func (m *Matcher) catalogMatches(ctx context.Context, s string) []Match {
	if m == nil || m.inert || m.detector == nil {
		return nil
	}
	if s == "" {
		return m.runCatalog(ctx, s)
	}
	key := screenCacheKeyFor(s)
	if hits, ok := m.screenCache.get(key); ok {
		return hits
	}
	hits := m.runCatalog(ctx, s)
	// Canceled passes contain partial evidence.
	if ctx.Err() == nil {
		m.screenCache.put(key, hits)
	}
	return hits
}
