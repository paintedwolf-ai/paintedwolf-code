// Package grounding holds the per-session evidence-grounding circuit-breaker state
// shared by the findings and delegation audit paths. It is storage-agnostic.
package grounding

import "sync"

// UngroundedCounter tracks circuit-breaker state per session.
type UngroundedCounter struct {
	TotalWarnings       int
	ConsecutiveWarnings int
	Escalated           bool
}

// StateStore tracks circuit-breaker counters per session.
type StateStore struct {
	mu     sync.Mutex
	bySess map[string]UngroundedCounter
}

// NewStateStore creates an empty grounding state store.
func NewStateStore() *StateStore {
	return &StateStore{bySess: make(map[string]UngroundedCounter)}
}

// Get returns counters for sessionID.
func (s *StateStore) Get(sessionID string) UngroundedCounter {
	if s == nil {
		return UngroundedCounter{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bySess[sessionID]
}

// Set stores counters for sessionID.
func (s *StateStore) Set(sessionID string, c UngroundedCounter) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bySess[sessionID] = c
}

// IsEscalated reports whether the session is blocked by the circuit breaker.
func (s *StateStore) IsEscalated(sessionID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bySess[sessionID].Escalated
}

// Reset clears circuit-breaker state for sessionID.
func (s *StateStore) Reset(sessionID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bySess, sessionID)
}

// ApplyUngroundedWarning updates circuit-breaker counters after an ungrounded claim.
func ApplyUngroundedWarning(state *UngroundedCounter, maxTotal, maxConsecutive int, escalateBlock bool) {
	if state == nil {
		return
	}
	state.TotalWarnings++
	state.ConsecutiveWarnings++
	if !escalateBlock {
		return
	}
	if state.TotalWarnings >= maxTotal || state.ConsecutiveWarnings >= maxConsecutive {
		state.Escalated = true
	}
}

// ResetUngroundedStreak clears consecutive warnings after a grounded claim.
func ResetUngroundedStreak(state *UngroundedCounter) {
	if state != nil {
		state.ConsecutiveWarnings = 0
	}
}
