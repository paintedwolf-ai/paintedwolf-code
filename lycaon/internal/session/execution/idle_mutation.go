package execution

import (
	"strings"
)

// TryIdleMutation acquires the per-session turn lock without blocking.
// Callers hold the lock for the complete idle mutation (bind, land, remove).
// ok is false when a turn already holds the same lock.
func (m *Lifetime) TryIdleMutation(sessionID string) (unlock func(), ok bool) {
	if m == nil {
		return nil, false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, false
	}
	lock := m.Prompt.Acquire(sessionID)
	if !lock.TryLock() {
		return nil, false
	}
	return func() { lock.Unlock() }, true
}

// HasRunningProcess reports whether sessionID owns a live command or terminal process.
func (m *Lifetime) HasRunningProcess(sessionID string) bool {
	if m == nil || m.processes == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	return sessionID != "" && m.processes.HasRunning(sessionID)
}
