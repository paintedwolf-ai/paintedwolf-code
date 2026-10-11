package session

import (
	"strings"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scratch"
)

// SetProjectRegistry wires project auto-name and touch helpers.
func (m *Host) SetProjectRegistry(registry project.Registry) {
	if m != nil {
		m.Coordinator.Tools.Projects = registry

		m.ProjectControl.SetProjects(registry)
		m.Profiles.SetProjects(registry)
		m.ToolContext.SetProjects(registry)
		m.Runner.Transcript.SetProjects(registry)
		m.Chats.Rewinds.SetProjects(registry)
		m.Chats.Naming.SetProjects(registry)
		m.Workspace.SetProjects(registry)
		m.Catalog.SetProjects(registry)
	}
}

// SetDataDir sets the data root for checkpoints and host data.
func (m *Host) SetDataDir(dir string) {
	if m != nil {
		m.Workspace.DataDir = strings.TrimSpace(dir)

		m.Coordinator.Tools.DataDir = strings.TrimSpace(dir)

		m.Chats.Captures.SetDataDir(m.Workspace.DataDir)
		m.Runner.History.SetDataDir(m.Workspace.DataDir)
		m.Verification.SetDataDir(m.Workspace.DataDir)
		m.ToolContext.SetDataDir(m.Workspace.DataDir)
	}
}

// SetScratchFolders supplies each session's private scratch folder.
func (m *Host) SetScratchFolders(folders *scratch.Folders) {
	if m != nil {
		m.Runner.Execution.SetScratch(folders)
		m.Chats.SetScratch(folders)
	}
}

// HostDataDirFor returns the project's host data directory under the data dir.
