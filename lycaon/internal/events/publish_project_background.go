package events

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// PublishProjectBackground emits a project-scoped background command lifecycle event.
func (p *Publisher) PublishProjectBackground(ctx context.Context, ev api.ProjectBackgroundEvent) {
	if p == nil || p.Hub == nil || strings.TrimSpace(ev.ProjectID) == "" {
		return
	}
	key := PublishKey{
		Project:        strings.TrimSpace(ev.ProjectID),
		EntityRevision: uint64(max(ev.Revision, 0)),
	}
	logPublishFailure(
		ctx,
		"PublishProjectBackground",
		api.EventTopicProjectBackground,
		p.Hub.Publish(ctx, api.EventTopicProjectBackground, key, ev),
	)
}
