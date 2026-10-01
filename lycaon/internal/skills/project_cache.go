package skills

import (
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// ProjectCacheTTL bounds how stale a project skills snapshot may be.
const ProjectCacheTTL = 30 * time.Second

// ProjectCache memoizes DiscoverProject per root set. The zero value is ready
// to use and safe for concurrent callers.
type ProjectCache struct {
	mu       sync.Mutex
	now      func() time.Time                              // tests override; nil means time.Now
	discover func(roots []string) ([]Skill, []ProjectNote) // tests override; nil means DiscoverProject
	entries  map[string]projectCacheEntry
	// group collapses concurrent misses for the same root set into one walk.
	group singleflight.Group
}

type projectCacheEntry struct {
	at     time.Time
	skills []Skill
	notes  []ProjectNote
}

type projectCacheDiscovery struct {
	skills []Skill
	notes  []ProjectNote
}

// Discover returns the cached DiscoverProject result for roots, refreshing it
// when older than ProjectCacheTTL. Callers receive fresh slice headers; the
// Skill values are shared and treated as read-only, like the device catalog.
func (c *ProjectCache) Discover(roots []string) ([]Skill, []ProjectNote) {
	key := strings.Join(roots, "\x00")

	if e, ok := c.lookupLocked(key); ok {
		return append([]Skill(nil), e.skills...), append([]ProjectNote(nil), e.notes...)
	}

	v, _, _ := c.group.Do(key, func() (any, error) {
		// A concurrent Do call for this key may have already populated the
		// entry while this call waited to enter the group.
		if e, ok := c.lookupLocked(key); ok {
			return projectCacheDiscovery{skills: e.skills, notes: e.notes}, nil
		}
		found, notes := c.discoverFunc()(roots)
		c.mu.Lock()
		if c.entries == nil {
			c.entries = map[string]projectCacheEntry{}
		}
		c.entries[key] = projectCacheEntry{at: c.clock(), skills: found, notes: notes}
		c.mu.Unlock()
		return projectCacheDiscovery{skills: found, notes: notes}, nil
	})
	result := v.(projectCacheDiscovery)
	return append([]Skill(nil), result.skills...), append([]ProjectNote(nil), result.notes...)
}

// lookupLocked returns the fresh cached entry for key, pruning expired
// entries for the whole cache first.
func (c *ProjectCache) lookupLocked(key string) (projectCacheEntry, bool) {
	now := c.clock()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)
	e, ok := c.entries[key]
	return e, ok
}

func (c *ProjectCache) discoverFunc() func([]string) ([]Skill, []ProjectNote) {
	if c.discover != nil {
		return c.discover
	}
	return DiscoverProject
}

func (c *ProjectCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *ProjectCache) pruneLocked(now time.Time) {
	for key, e := range c.entries {
		if now.Sub(e.at) >= ProjectCacheTTL {
			delete(c.entries, key)
		}
	}
}
