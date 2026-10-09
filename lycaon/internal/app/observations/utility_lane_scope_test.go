package observations

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/pkg/api"
)

type utilityScopeHub struct {
	events.EventHub
	key   events.PublishKey
	topic api.EventTopic
}

func (h *utilityScopeHub) Publish(_ context.Context, topic api.EventTopic, key events.PublishKey, _ any) error {
	h.key, h.topic = key, topic
	return events.ValidatePublishScope(topic, key)
}

func TestUtilityLanePreservesProjectIdentity(t *testing.T) {
	for _, projectID := range []string{testdbseed.DefaultProjectID, ""} {
		t.Run("project="+projectID, func(t *testing.T) {
			hub := &utilityScopeHub{}
			publisher := utilityLanePublisher{pub: &events.Publisher{Hub: hub}}
			ctx := curationctx.WithSession(t.Context(), curationctx.Session{SessionID: "session", ProjectID: projectID, ProjectDir: t.TempDir()})
			publisher.PublishCall(ctx, api.LLMCallEvent{})
			if hub.topic != api.EventTopicLLM || hub.key.Project != projectID || hub.key.Session != "session" {
				t.Fatalf("utility event scope = %+v, topic = %s", hub.key, hub.topic)
			}
		})
	}
}
