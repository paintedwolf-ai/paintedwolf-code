package events

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/pkg/api"
)

// DebugHub wraps a hub and mirrors publishes to LYCAON_SSE_DEBUG_FILE when enabled.
type DebugHub struct {
	inner ReplayHub
}

// WrapDebugHub returns inner unchanged when SSE debug is disabled.
func WrapDebugHub(inner ReplayHub) ReplayHub {
	if inner == nil || !observability.SSEDebugEnabled() {
		return inner
	}
	return &DebugHub{inner: inner}
}

func (h *DebugHub) Publish(ctx context.Context, topic api.EventTopic, key PublishKey, data any) error {
	if err := ValidatePublishScope(topic, key); err != nil {
		return err
	}
	raw, err := json.Marshal(data)
	if err == nil {
		observability.LogSSEPublish(topic, key.Project, key.Session, api.EventEnvelope{
			V:           1,
			Topic:       topic,
			PublishedAt: time.Now().UTC(),
			Scope:       key.Scope(),
			Data:        raw,
		})
	}
	return h.inner.Publish(ctx, topic, key, data)
}

func (h *DebugHub) Subscribe(ctx context.Context, sub Subscription) (<-chan api.EventEnvelope, func(), error) {
	return h.inner.Subscribe(ctx, sub)
}

func (h *DebugHub) CurrentCursor() string {
	return h.inner.CurrentCursor()
}

func (h *DebugHub) SubscriberCount() int {
	return h.inner.SubscriberCount()
}

var _ EventHub = (*DebugHub)(nil)
var _ ReplayHub = (*DebugHub)(nil)
