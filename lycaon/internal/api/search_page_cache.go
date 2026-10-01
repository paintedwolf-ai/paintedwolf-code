package api

import (
	"sync"
	"time"

	wire "github.com/lycaon/lycaon/pkg/api"
)

const (
	searchPageLifetime       = 10 * time.Minute
	searchPageMaxGenerations = 8
)

type searchPageEntry struct {
	scope     string
	response  wire.SearchResponse
	expiresAt time.Time
}

type searchPageCache struct {
	mu         sync.Mutex
	generation uint64
	entries    map[uint64]searchPageEntry
}

func newSearchPageCache() *searchPageCache {
	return &searchPageCache{entries: make(map[uint64]searchPageEntry)}
}

func (c *searchPageCache) put(scope string, response wire.SearchResponse) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(time.Now())
	c.generation++
	c.entries[c.generation] = searchPageEntry{scope: scope, response: response, expiresAt: time.Now().Add(searchPageLifetime)}
	for len(c.entries) > searchPageMaxGenerations {
		oldest := c.generation
		for generation := range c.entries {
			if generation < oldest {
				oldest = generation
			}
		}
		delete(c.entries, oldest)
	}
	return c.generation
}

func (c *searchPageCache) get(generation uint64, scope string) (wire.SearchResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(time.Now())
	entry, ok := c.entries[generation]
	if !ok || entry.scope != scope {
		return wire.SearchResponse{}, false
	}
	entry.expiresAt = time.Now().Add(searchPageLifetime)
	c.entries[generation] = entry
	return entry.response, true
}

func (c *searchPageCache) pruneLocked(now time.Time) {
	for generation, entry := range c.entries {
		if !entry.expiresAt.After(now) {
			delete(c.entries, generation)
		}
	}
}
