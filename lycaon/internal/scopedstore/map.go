package scopedstore

import (
	"maps"
	"sync"
)

// Map is a concurrency-safe map for state that is not re-derivable, such as
// authority a person granted for a chat. Nothing is evicted; the owner deletes
// a key when its scope ends.
type Map[T any] struct {
	mu      sync.Mutex
	entries map[string]T
}

// Store inserts or replaces one key.
func (m *Map[T]) Store(key string, value T) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = make(map[string]T)
	}
	m.entries[key] = value
}

// Load returns one value.
func (m *Map[T]) Load(key string) (T, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.entries[key]
	return value, ok
}

// Delete removes one key.
func (m *Map[T]) Delete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, key)
}

// Snapshot returns a shallow copy.
func (m *Map[T]) Snapshot() map[string]T {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.entries)
}
