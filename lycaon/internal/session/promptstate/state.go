package promptstate

import (
	"sync"
)

// MutexRegistry holds one reference-counted mutex per id, dropped when the
// last holder releases it.
type MutexRegistry struct {
	mu      sync.Mutex
	entries map[string]*mutexEntry
}

type mutexEntry struct {
	mu   sync.Mutex
	refs int
}

type Lock struct {
	registry *MutexRegistry
	id       string
	entry    *mutexEntry
	released bool
}

// Acquire references id's mutex; the caller locks or try-locks it.
func (r *MutexRegistry) Acquire(id string) *Lock {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = make(map[string]*mutexEntry)
	}
	entry := r.entries[id]
	if entry == nil {
		entry = &mutexEntry{}
		r.entries[id] = entry
	}
	entry.refs++
	return &Lock{registry: r, id: id, entry: entry}
}

func (m *Lock) Lock() {
	m.entry.mu.Lock()
}

func (m *Lock) TryLock() bool {
	if m.entry.mu.TryLock() {
		return true
	}
	m.release()
	return false
}

func (m *Lock) Unlock() {
	m.entry.mu.Unlock()
	m.release()
}

func (m *Lock) release() {
	if m.released {
		return
	}
	m.released = true
	r := m.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	m.entry.refs--
	if m.entry.refs == 0 && r.entries[m.id] == m.entry {
		delete(r.entries, m.id)
	}
}

func (r *MutexRegistry) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}
