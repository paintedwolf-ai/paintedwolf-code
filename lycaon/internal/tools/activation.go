package tools

import (
	"strings"
	"sync"
)

// MemoryActivation is the SchemaActivation of a runtime with no session
// ledger: an isolated tool host, a harness, or a test. The app replaces it
// with the coordinator's turn load ledger.
type MemoryActivation struct {
	mu     sync.Mutex
	active map[string]map[string]bool
}

// NewMemoryActivation returns an empty in-memory activation set.
func NewMemoryActivation() *MemoryActivation {
	return &MemoryActivation{active: map[string]map[string]bool{}}
}

// Active returns the loaded tool names for sessionID, or nil.
func (m *MemoryActivation) Active(sessionID string) map[string]bool {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.active[strings.TrimSpace(sessionID)]
	if len(set) == 0 {
		return nil
	}
	out := make(map[string]bool, len(set))
	for name := range set {
		out[name] = true
	}
	return out
}

// Activate records names as loaded for sessionID.
func (m *MemoryActivation) Activate(sessionID string, names []string, _ string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sessionID = strings.TrimSpace(sessionID)
	set := m.active[sessionID]
	if set == nil {
		set = map[string]bool{}
		m.active[sessionID] = set
	}
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			set[name] = true
		}
	}
}
