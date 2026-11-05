package eventing

import (
	"context"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/pkg/api"
)

type PreparationObserver interface {
	Observe(api.EventTopic, events.PublishKey, any)
}

type preparationHub struct {
	events.ReplayHub
	observer PreparationObserver
}

func (h *preparationHub) Publish(ctx context.Context, topic api.EventTopic, key events.PublishKey, data any) error {
	if err := h.ReplayHub.Publish(ctx, topic, key, data); err != nil {
		return err
	}
	h.observer.Observe(topic, key, data)
	return nil
}
