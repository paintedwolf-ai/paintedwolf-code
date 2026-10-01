package survey

import (
	"strings"
	"sync"
)

// ReadEscalationStore tracks unbounded read strikes per (session_id, path).
type ReadEscalationStore struct {
	mu    sync.Mutex
	count map[string]int
}

// NewReadEscalationStore returns an empty escalation tracker.
func NewReadEscalationStore() *ReadEscalationStore {
	return &ReadEscalationStore{count: make(map[string]int)}
}

func readEscalationKey(sessionID, path string) string {
	return strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(path)
}

// BumpUnboundedRead increments the strike counter and returns the new count.
func (s *ReadEscalationStore) BumpUnboundedRead(sessionID, path string) int {
	if s == nil {
		return 1
	}
	key := readEscalationKey(sessionID, path)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count[key]++
	return s.count[key]
}

// Strike returns the current strike count without incrementing.
func (s *ReadEscalationStore) Strike(sessionID, path string) int {
	if s == nil {
		return 0
	}
	key := readEscalationKey(sessionID, path)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count[key]
}
