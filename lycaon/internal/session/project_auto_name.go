package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scratch"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// SetProjectRegistry wires project auto-name and touch helpers.
func (m *Manager) SetProjectRegistry(registry project.Registry) {
	if m != nil {
		m.projects = registry
		m.catalog.SetProjects(registry)
	}
}

// SetDataDir sets the data root for checkpoints and host data.
func (m *Manager) SetDataDir(dir string) {
	if m != nil {
		m.dataDir = strings.TrimSpace(dir)
	}
}

// SetScratchFolders supplies each session's private scratch folder.
func (m *Manager) SetScratchFolders(folders *scratch.Folders) {
	if m != nil {
		m.scratch = folders
	}
}

// HostDataDirFor returns the project's host data directory under the data dir.
func (m *Manager) HostDataDirFor(projectID string) string {
	if m == nil {
		return project.HostDataDir("", projectID)
	}
	return project.HostDataDir(m.dataDir, projectID)
}

// autoNameProjectFromPrompt persists a draft project name via UpdateNameIfUnset.
func (m *Manager) autoNameProjectFromPrompt(ctx context.Context, sess *wire.Session, promptText string) {
	if m == nil || m.projects == nil || sess == nil {
		return
	}
	text := strings.TrimSpace(promptText)
	if text == "" || sess.IsWorkerChild() {
		return
	}
	projectID := strings.TrimSpace(sess.ProjectID)
	if projectID == "" {
		return
	}
	p, err := m.projects.Get(ctx, projectID)
	if err != nil || p == nil || !p.IsDraft || strings.TrimSpace(p.Name) != "" {
		return
	}
	projectDir := m.overlayProjectDir(ctx, sess)
	name := project.NameProject(ctx, m.sessionNamer(sess, "project_name", projectDir), text)
	if name == "" {
		return
	}
	updated, err := m.projects.UpdateNameIfUnset(ctx, projectID, name)
	if err != nil || !updated {
		return
	}
	m.publishProjectUpdated(ctx, projectID)
}

func (m *Manager) publishProjectUpdated(ctx context.Context, projectID string) {
	if m == nil || m.events == nil || m.events.Hub == nil || m.projects == nil {
		return
	}
	if m.projects.MutationEventsOutboxed() {
		return
	}
	p, err := m.projects.Get(ctx, projectID)
	if err != nil || p == nil {
		return
	}
	apiProj := project.ToAPI(p)
	ev := wire.ProjectEvent{
		ID:      p.ID,
		Action:  wire.ProjectEventUpdated,
		Project: &apiProj,
	}
	key := events.PublishKey{Project: p.ID}
	_ = m.events.Hub.Publish(ctx, wire.EventTopicProject, key, ev)
}
