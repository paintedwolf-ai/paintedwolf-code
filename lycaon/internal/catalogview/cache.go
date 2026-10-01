package catalogview

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/lycaon/lycaon/internal/extpacks"
)

// LRUCap is the hard bound on cached views.
const LRUCap = 8

// Cache builds Views once per catalog Revision behind an LRU of LRUCap.
type Cache struct {
	moduleRoot string
	log        *slog.Logger

	mu       sync.Mutex
	entries  map[string]*cacheEntry
	order    []string // oldest → newest
	builds   int      // successful Build calls (tests)
	attempts int      // every Build call, including failures (tests)
	failLog  map[string]struct{}
}

type cacheEntry struct {
	view *View
	err  error
	done chan struct{} // closed when the in-flight build finishes
}

// NewCache returns a revision-keyed view cache for moduleRoot.
func NewCache(moduleRoot string, log *slog.Logger) *Cache {
	if log == nil {
		log = slog.Default()
	}
	return &Cache{
		moduleRoot: moduleRoot,
		log:        log,
		entries:    map[string]*cacheEntry{},
		failLog:    map[string]struct{}{},
	}
}

// BuildCount returns how many successful Build calls this cache has made.
func (c *Cache) BuildCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.builds
}

// AttemptCount returns how many Build calls this cache has made, including failures.
func (c *Cache) AttemptCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempts
}

// Len returns how many views are currently cached.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.entries {
		if e != nil && e.view != nil {
			n++
		}
	}
	return n
}

// For caches successful and failed compilations by revision; a nil catalog is invalid.
func (c *Cache) For(ctx context.Context, eff *extpacks.EffectiveCatalog) (*View, error) {
	if c == nil {
		return nil, fmt.Errorf("catalogview: nil cache")
	}
	if eff == nil {
		return nil, fmt.Errorf("catalogview: nil effective catalog")
	}
	key := eff.Revision
	if key == "" {
		return nil, fmt.Errorf("catalogview: empty catalog revision")
	}

	for {
		c.mu.Lock()
		if e, ok := c.entries[key]; ok {
			if e.view != nil {
				c.touchLocked(key)
				v := e.view
				c.mu.Unlock()
				return v, nil
			}
			if e.err != nil {
				c.mu.Unlock()
				return nil, e.err
			}
			if e.done != nil {
				done := e.done
				c.mu.Unlock()
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-done:
				}
				continue
			}
		}

		e := &cacheEntry{done: make(chan struct{})}
		c.entries[key] = e
		c.mu.Unlock()

		view, err := Build(ctx, c.moduleRoot, eff)

		c.mu.Lock()
		c.attempts++
		if err != nil {
			e.err = err
			c.logFailOnceLocked(key, err)
			c.touchLocked(key)
			c.evictLocked()
			close(e.done)
			c.mu.Unlock()
			return nil, err
		}
		e.view = view
		c.builds++
		c.touchLocked(key)
		c.evictLocked()
		close(e.done)
		c.mu.Unlock()
		return view, nil
	}
}

func (c *Cache) touchLocked(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, key)
}

func (c *Cache) evictLocked() {
	for len(c.order) > LRUCap {
		old := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, old)
	}
}

func (c *Cache) logFailOnceLocked(key string, err error) {
	if _, ok := c.failLog[key]; ok {
		return
	}
	c.failLog[key] = struct{}{}
	prefix := key
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	c.log.Warn("catalog view build failed", "revision", prefix, "error", err)
}
