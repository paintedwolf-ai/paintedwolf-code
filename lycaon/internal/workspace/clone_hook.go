package workspace

import "github.com/lycaon/lycaon/internal/layeredfs/adapter"

// cloneWorkspaceFile replaces file cloning for every manager without its own cloner.
var cloneWorkspaceFile adapter.FileCloner

func (m *Manager) fileCloner() adapter.FileCloner {
	if m.cloneFile != nil {
		return m.cloneFile
	}
	return cloneWorkspaceFile
}
