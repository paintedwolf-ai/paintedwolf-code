package surface

import (
	"strings"
	"sync"
)

// ExecutionModeStateStore keeps the last mode family for each coordinator session.
type ExecutionModeStateStore struct {
	mu         sync.Mutex
	lastFamily map[string]string
}

// NewExecutionModeStateStore constructs an in-memory execution-mode state store.
func NewExecutionModeStateStore() *ExecutionModeStateStore {
	return &ExecutionModeStateStore{lastFamily: make(map[string]string)}
}

// Load returns persisted execution-mode state for sessionID.
func (s *ExecutionModeStateStore) Load(sessionID string) ExecutionModeState {
	if s == nil || sessionID == "" {
		return ExecutionModeState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return ExecutionModeState{LastFamily: strings.TrimSpace(s.lastFamily[sessionID])}
}

// Save records the current execution-mode family at end of coordinator turn assembly.
func (s *ExecutionModeStateStore) Save(sessionID, family string) {
	if s == nil || sessionID == "" {
		return
	}
	s.mu.Lock()
	s.lastFamily[sessionID] = strings.TrimSpace(family)
	s.mu.Unlock()
}
