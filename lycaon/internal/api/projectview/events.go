// Package projectview publishes project and settings change events.
package projectview

import (
	"context"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// PublishTouch announces that a project changed without new identity.
func PublishTouch(registry project.Registry, hub events.ReplayHub, ctx context.Context, projectID string) {
	if registry == nil || hub == nil {
		return
	}
	p, err := registry.Get(ctx, projectID)
	if err != nil {
		return
	}
	PublishEvent(registry, hub, ctx, wire.ProjectEventUpdated, p)
}

func PublishEvent(registry project.Registry, hub events.ReplayHub, ctx context.Context, action wire.ProjectEventAction, p *project.Project) {
	if hub == nil || p == nil {
		return
	}
	if registry.MutationEventsOutboxed() {
		return
	}
	ev := wire.ProjectEvent{
		ID:     p.ID,
		Action: action,
	}
	if action != wire.ProjectEventDeleted {
		apiProj := project.ToAPI(p)
		ev.Project = &apiProj
	}
	key := events.PublishKey{Project: p.ID}
	_ = hub.Publish(ctx, wire.EventTopicProject, key, ev)
}

func PublishSettings(hub events.ReplayHub, registry project.Registry, ctx context.Context, area wire.SettingsArea, scope, projectDir, action string) {
	if hub == nil {
		return
	}
	key := events.PublishKeyFor(ctx, project.ScopeLookup{Registry: registry}, projectDir, "")
	key.Facet = string(area)
	_ = hub.Publish(ctx, wire.EventTopicSettings, key, wire.SettingsEvent{
		Area:   area,
		Scope:  wire.SettingsScope(scope),
		Action: action,
	})
}
