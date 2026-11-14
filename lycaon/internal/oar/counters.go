package oar

import (
	"encoding/json"
	"reflect"
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
	// runs holds the trailing runs of each session's earlier occurrences per
	// anchor ([OAR-FIRE-8], [OAR-FIRE-9]).
	runs map[string]*trailingRuns         // key = sessionID + "\x00" + anchor
	data map[string]map[CounterKind]int64 // key = sessionID + "\x00" + code
}

// trailingRuns is the state the run counts need: the latest blocks carrying
// one code, and the latest occurrences of one call.
type trailingRuns struct {
	blockCode  string
	blockRun   int64
	call       any // the latest call as a JSON value
	callStreak int64
}

type occurrenceLock struct {
	mu    sync.Mutex
	users int
}

// NewCounterStore returns an empty store.
func NewCounterStore() *CounterStore {
	return &CounterStore{
		data: map[string]map[CounterKind]int64{}, history: map[string][]string{},
		runs: map[string]*trailingRuns{}, occurrences: map[string]*occurrenceLock{},
	}
}

func counterKey(sessionID, code string) string {
	return sessionID + "\x00" + code
}

// CloneSession copies every counter key touched in session, for the
// occurrence-scoped snapshot [OAR-FIRE-5] requires.
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
// in the fixture shape ([OAR-CONF-15]).
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
	for key := range s.runs {
		if strings.HasPrefix(key, prefix) {
			delete(s.runs, key)
		}
	}
}

// applyRunCounts sets a new occurrence's run counts from the session's earlier
// occurrences at the same anchor.
func (s *CounterStore) applyRunCounts(gc *GuardContext, anchor string) {
	if s == nil || gc == nil {
		return
	}
	s.mu.Lock()
	runs := s.runs[counterKey(gc.Session.SessionID, anchor)]
	s.mu.Unlock()
	gc.Counters.SameCodeRejectRun, gc.Counters.SameCallRun = 0, 0
	if runs == nil {
		return
	}
	gc.Counters.SameCodeRejectRun = runs.blockRun
	if reflect.DeepEqual(runs.call, callValue(gc)) {
		gc.Counters.SameCallRun = runs.callStreak
	}
}

// recordOccurrence advances the trailing runs with an occurrence's decision
// and call.
func (s *CounterStore) recordOccurrence(gc *GuardContext, anchor string, decision *Decision) {
	if s == nil || gc == nil {
		return
	}
	call := callValue(gc)
	key := counterKey(gc.Session.SessionID, anchor)
	s.mu.Lock()
	defer s.mu.Unlock()
	runs := s.runs[key]
	if runs == nil {
		runs = &trailingRuns{}
		s.runs[key] = runs
	}
	switch {
	case decision == nil || decision.Effect != EffectBlock:
		runs.blockCode, runs.blockRun = "", 0
	case runs.blockRun > 0 && runs.blockCode == decision.Code:
		runs.blockRun++
	default:
		runs.blockCode, runs.blockRun = decision.Code, 1
	}
	if runs.callStreak > 0 && reflect.DeepEqual(runs.call, call) {
		runs.callStreak++
	} else {
		runs.call, runs.callStreak = call, 1
	}
}

// callValue is an occurrence's call as a JSON value, so arguments compare as
// JSON values whatever their member order or number representation
// ([OAR-FACT-20]).
func callValue(gc *GuardContext) any {
	raw, err := json.Marshal(map[string]any{"tool": gc.Invocation.Tool, "args": mapOrEmpty(gc.Invocation.ToolArgs)})
	if err != nil {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return v
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
