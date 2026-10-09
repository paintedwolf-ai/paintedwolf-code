package promptstate

import (
	"context"
	"strings"
	"sync"
)

// State keeps prompt exclusion, durable submission ordering, and cancellation scopes.
// Its zero value is ready for use and must not be copied after use.
type State struct {
	// Prompt serializes one session's prompt turns.
	Prompt MutexRegistry
	// Clock serializes one root session's turn clock.
	Clock MutexRegistry
	// Submission orders one session's durable prompt submissions.
	Submission     MutexRegistry
	operationMu    sync.Mutex
	operationLocks map[string]*operationLock
	promptCancelMu sync.Mutex
	promptCancel   map[string]context.CancelFunc
	stopped        bool
}

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

type operationLock struct {
	mu   sync.Mutex
	refs int
}

// LockOperation serializes one idempotency key.
func (m *State) LockOperation(operationID string) func() {
	operationID = strings.TrimSpace(operationID)
	m.operationMu.Lock()
	if m.operationLocks == nil {
		m.operationLocks = make(map[string]*operationLock)
	}
	entry := m.operationLocks[operationID]
	if entry == nil {
		entry = &operationLock{}
		m.operationLocks[operationID] = entry
	}
	entry.refs++
	m.operationMu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		m.operationMu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(m.operationLocks, operationID)
		}
		m.operationMu.Unlock()
	}
}

func (m *State) Cancel(sessionID string) {
	if m == nil {
		return
	}
	m.promptCancelMu.Lock()
	cancel, ok := m.promptCancel[sessionID]
	if ok {
		delete(m.promptCancel, sessionID)
	}
	m.promptCancelMu.Unlock()
	if ok && cancel != nil {
		cancel()
	}
}

func (m *State) RegisterCancel(sessionID string, cancel context.CancelFunc) {
	if m == nil || cancel == nil {
		return
	}
	m.promptCancelMu.Lock()
	if m.stopped {
		m.promptCancelMu.Unlock()
		cancel()
		return
	}
	defer m.promptCancelMu.Unlock()
	if m.promptCancel == nil {
		m.promptCancel = make(map[string]context.CancelFunc)
	}
	if prev, ok := m.promptCancel[sessionID]; ok {
		prev()
	}
	m.promptCancel[sessionID] = cancel
}

func (m *State) Running(sessionID string) bool {
	if m == nil {
		return false
	}
	m.promptCancelMu.Lock()
	defer m.promptCancelMu.Unlock()
	_, ok := m.promptCancel[sessionID]
	return ok
}

// Stop seals registration and cancels every prompt's independent stop context.
func (m *State) Stop() {
	m.promptCancelMu.Lock()
	m.stopped = true
	cancels := m.promptCancel
	m.promptCancel = nil
	m.promptCancelMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}
