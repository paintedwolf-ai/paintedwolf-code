package naming

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// ProjectFromPrompt persists a draft project name via UpdateNameIfUnset.
func (m *Service) ProjectFromPrompt(ctx context.Context, sess *wire.Session, promptText string) {
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
	projectDir := m.roots.SettingsPath(ctx, sess)
	name := project.NameProject(ctx, m.namer(sess, "project_name", projectDir), text)
	if name == "" {
		return
	}
	updated, err := m.projects.UpdateNameIfUnset(ctx, projectID, name)
	if err != nil || !updated {
		return
	}
	m.PublishProject(ctx, projectID)
}

func (m *Service) PublishProject(ctx context.Context, projectID string) {
	if m == nil || m.publisher == nil || m.publisher.Hub == nil || m.projects == nil {
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
	_ = m.publisher.Hub.Publish(ctx, wire.EventTopicProject, key, ev)
}
