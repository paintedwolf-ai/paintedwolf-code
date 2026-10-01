package oar

import (
	"strings"
	"sync"
)

// CounterKind identifies an engine-managed counter fact.
type CounterKind string

const (
	CounterFire    CounterKind = "fire_count"
	CounterBreaker CounterKind = "breaker_count"
)

// CounterStore stores per-session rule counters.
type CounterStore struct {
	mu          sync.Mutex
	occurrences map[string]*occurrenceLock
	history     map[string][]string
	data        map[string]map[CounterKind]int64 // key = sessionID + "\x00" + code
}

type occurrenceLock struct {
	mu    sync.Mutex
	users int
}

// NewCounterStore returns an empty store.
func NewCounterStore() *CounterStore {
	return &CounterStore{data: map[string]map[CounterKind]int64{}, history: map[string][]string{}, occurrences: map[string]*occurrenceLock{}}
}

func counterKey(sessionID, code string) string {
	return sessionID + "\x00" + code
}

// CloneSession copies every counter key touched in session, for the
// occurrence-scoped snapshot [OAR-FIRE-6] requires.
func (s *CounterStore) CloneSession(sessionID string) map[string]map[CounterKind]int64 {
	out := map[string]map[CounterKind]int64{}
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := sessionID + "\x00"
	for key, m := range s.data {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		id := key
		if i := strings.Index(key, "\x00"); i >= 0 {
			id = key[i+1:]
		}
		cp := make(map[CounterKind]int64, len(m))
		for k, v := range m {
			cp[k] = v
		}
		out[id] = cp
	}
	return out
}

// Get returns the counter value (0 if unset).
func (s *CounterStore) Get(sessionID, code string, kind CounterKind) int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.data[counterKey(sessionID, code)]
	if m == nil {
		return 0
	}
	return m[kind]
}

// Increment adds delta (usually 1) and returns the new value.
func (s *CounterStore) Increment(sessionID, code string, kind CounterKind, delta int64) int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := counterKey(sessionID, code)
	m := s.data[key]
	if m == nil {
		m = map[CounterKind]int64{}
		s.data[key] = m
	}
	m[kind] += delta
	return m[kind]
}

// Reset sets the counter to 0.
func (s *CounterStore) Reset(sessionID, code string, kind CounterKind) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := counterKey(sessionID, code)
	m := s.data[key]
	if m == nil {
		m = map[CounterKind]int64{}
		s.data[key] = m
	}
	m[kind] = 0
}

// Report returns fire_count / breaker_count for every key touched in session,
// in the fixture shape ([OAR-CONF-21], [OAR-CONF-33]).
func (s *CounterStore) Report(sessionID string) map[string]map[string]int {
	out := map[string]map[string]int{}
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := sessionID + "\x00"
	for key, m := range s.data {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		id := key
		if i := strings.Index(key, "\x00"); i >= 0 {
			id = key[i+1:]
		}
		out[id] = map[string]int{"fire_count": int(m[CounterFire]), "breaker_count": int(m[CounterBreaker])}
	}
	return out
}

// ForgetSession removes only this session's observations and counters.
func (s *CounterStore) ForgetSession(sessionID string) {
	if s == nil {
		return
	}
	unlock := s.beginOccurrence(sessionID)
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := sessionID + "\x00"
	for key := range s.data {
		if strings.HasPrefix(key, prefix) {
			delete(s.data, key)
		}
	}
	delete(s.history, sessionID)
}

// beginOccurrence serializes one session's snapshot and side-effects.
func (s *CounterStore) beginOccurrence(sessionID string) func() {
	s.mu.Lock()
	lock := s.occurrences[sessionID]
	if lock == nil {
		lock = &occurrenceLock{}
		s.occurrences[sessionID] = lock
	}
	lock.users++
	s.mu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		s.mu.Lock()
		defer s.mu.Unlock()
		lock.users--
		if lock.users == 0 {
			delete(s.occurrences, sessionID)
		}
	}
}
