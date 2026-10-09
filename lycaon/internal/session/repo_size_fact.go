package session

import (
	"github.com/lycaon/lycaon/internal/repoinfo"
)

// SetRepoProvider wires progressive-brief file_count into ToolContext.
func (m *Manager) SetRepoProvider(p repoinfo.Provider) {
	if m == nil {
		return
	}
	m.repoProvider = p
	m.Guards.SetRepoProvider(p)
	m.ToolContext.SetRepoProvider(p)
}

// repoinfo.MeasuredEmpty reports a measured empty tree for task() gates.
// Unmeasured trees return false.
