// Package scopedstore provides bounded maps for runtime-scoped state.
package scopedstore

import (
	"container/list"
	"sync"
)

// DefaultEntries bounds re-derivable state keyed by runtime IDs.
const DefaultEntries = 256

// LRU is a capacity-bounded, concurrency-safe map keyed by a runtime identity.
type LRU[T any] struct {
	mu       sync.Mutex
	entries  map[string]*list.Element
	order    *list.List // front is most recently touched
	capacity int
}

type node[T any] struct {
	key   string
	value T
}

// New returns an LRU with a minimum capacity of one.
func New[T any](capacity int) *LRU[T] {
	c := &LRU[T]{}
	c.ensure()
	if capacity >= 1 {
		c.capacity = capacity
	}
	return c
}

// ensure makes the zero value usable without a constructor.
func (c *LRU[T]) ensure() {
	if c.order == nil {
		c.order = list.New()
		c.entries = make(map[string]*list.Element)
	}
	if c.capacity < 1 {
		c.capacity = DefaultEntries
	}
}

// Store inserts or refreshes one key, evicting the least recently touched
// entries past capacity.
func (c *LRU[T]) Store(key string, value T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	if el, ok := c.entries[key]; ok {
		el.Value.(*node[T]).value = value
		c.order.MoveToFront(el)
		return
	}
	c.entries[key] = c.order.PushFront(&node[T]{key: key, value: value})
	c.evictLocked()
}

// evictLocked drops least-recently-used entries past capacity.
func (c *LRU[T]) evictLocked() {
	for c.order.Len() > c.capacity {
		oldest := c.order.Back()
		if oldest == nil {
			return
		}
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*node[T]).key)
	}
}

// Load returns a value and refreshes its recency.
func (c *LRU[T]) Load(key string) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	el, ok := c.entries[key]
	if !ok {
		var zero T
		return zero, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*node[T]).value, true
}

// LoadOrStore returns the existing value for a key, or stores and returns the
// given one. loaded reports whether the key was already present.
func (c *LRU[T]) LoadOrStore(key string, value T) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*node[T]).value, true
	}
	c.entries[key] = c.order.PushFront(&node[T]{key: key, value: value})
	c.evictLocked()
	return value, false
}

// LoadAndDelete returns a value and removes it.
func (c *LRU[T]) LoadAndDelete(key string) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	el, ok := c.entries[key]
	if !ok {
		var zero T
		return zero, false
	}
	value := el.Value.(*node[T]).value
	c.order.Remove(el)
	delete(c.entries, key)
	return value, true
}

// Delete removes one key.
func (c *LRU[T]) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	el, ok := c.entries[key]
	if !ok {
		return
	}
	c.order.Remove(el)
	delete(c.entries, key)
}

// Len reports how many entries the store holds.
func (c *LRU[T]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	return c.order.Len()
}

// Snapshot returns a shallow copy without changing recency.
func (c *LRU[T]) Snapshot() map[string]T {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	out := make(map[string]T, len(c.entries))
	for key, el := range c.entries {
		out[key] = el.Value.(*node[T]).value
	}
	return out
}

// Clear empties the store in place.
func (c *LRU[T]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	clear(c.entries)
	c.order.Init()
}
