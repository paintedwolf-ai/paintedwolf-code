package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/repoinfo"
)

// SetRepoProvider wires progressive-brief file_count into ToolContext.
func (m *Manager) SetRepoProvider(p repoinfo.Provider) {
	if m == nil {
		return
	}
	m.repoProvider = p
}

// sessionWorkspaceKnownEmpty reports a measured empty tree for task() gates.
// Unmeasured trees return false.
func sessionWorkspaceKnownEmpty(ctx context.Context, p repoinfo.Provider, workspacePath string) bool {
	if p == nil || workspacePath == "" {
		return false
	}
	empty, err := p.KnownEmpty(ctx, workspacePath)
	return err == nil && empty
}
