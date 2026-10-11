package session

import (
	"github.com/lycaon/lycaon/internal/repoinfo"
)

// SetRepoProvider wires progressive-brief file_count into ToolContext.
func (m *Host) SetRepoProvider(p repoinfo.Provider) {
	if m == nil {
		return
	}
	m.Coordinator.Assembly.Repository = p

	m.Coordinator.Guards.SetRepoProvider(p)
	m.ToolContext.SetRepoProvider(p)
}

// repoinfo.MeasuredEmpty reports a measured empty tree for task() gates.
// Unmeasured trees return false.
