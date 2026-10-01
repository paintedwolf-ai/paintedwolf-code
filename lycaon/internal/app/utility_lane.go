package app

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/pkg/api"
)

type utilityLanePublisher struct {
	pub *events.Publisher
}

func (p utilityLanePublisher) PublishCall(ctx context.Context, ev api.LLMCallEvent) {
	if p.pub == nil {
		return
	}
	sess := curationctx.SessionFrom(ctx)
	sessionID := strings.TrimSpace(sess.SessionID)
	if sessionID == "" {
		return
	}
	project := strings.TrimSpace(sess.ProjectID)
	p.pub.PublishLLM(ctx, project, sessionID, ev)
}
