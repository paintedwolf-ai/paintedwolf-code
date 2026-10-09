package session

import (
	"strings"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scratch"
)

// SetProjectRegistry wires project auto-name and touch helpers.
func (m *Manager) SetProjectRegistry(registry project.Registry) {
	if m != nil {
		m.projects = registry
		m.ProjectControl.SetProjects(registry)
		m.Profiles.SetProjects(registry)
		m.ToolContext.SetProjects(registry)
		m.Transcript.SetProjects(registry)
		m.Rewinds.SetProjects(registry)
		m.Naming.SetProjects(registry)
		m.Workspace.SetProjects(registry)
		m.Catalog.SetProjects(registry)
	}
}

// SetDataDir sets the data root for checkpoints and host data.
func (m *Manager) SetDataDir(dir string) {
	if m != nil {
		m.dataDir = strings.TrimSpace(dir)
		m.Captures.SetDataDir(m.dataDir)
		m.Runner.History.SetDataDir(m.dataDir)
		m.Verification.SetDataDir(m.dataDir)
		m.ToolContext.SetDataDir(m.dataDir)
	}
}

// SetScratchFolders supplies each session's private scratch folder.
func (m *Manager) SetScratchFolders(folders *scratch.Folders) {
	if m != nil {
		m.Runner.Execution.SetScratch(folders)
		m.Chats.SetScratch(folders)
	}
}

// HostDataDirFor returns the project's host data directory under the data dir.
func (m *Manager) HostDataDirFor(projectID string) string {
	if m == nil {
		return project.HostDataDir("", projectID)
	}
	return project.HostDataDir(m.dataDir, projectID)
}
