package eventing

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/pkg/api"
)

type observedPreparation struct{ calls int }

func (o *observedPreparation) Observe(api.EventTopic, events.PublishKey, any) { o.calls++ }

type refusedPreparationHub struct{ events.ReplayHub }

func (h refusedPreparationHub) Publish(context.Context, api.EventTopic, events.PublishKey, any) error {
	return errors.New("publication refused")
}

func TestPreparationObservesCommittedEventsWithoutPresence(t *testing.T) {
	observer := &observedPreparation{}
	hub := &preparationHub{ReplayHub: events.NewMemoryHub(), observer: observer}
	if err := hub.Publish(t.Context(), api.EventTopicSettings, events.PublishKey{}, struct{}{}); err != nil {
		t.Fatalf("publish settings: %v", err)
	}
	if observer.calls != 1 || hub.SubscriberCount() != 0 {
		t.Fatal("preparation observation changed presence or missed publication")
	}
	hub.ReplayHub = refusedPreparationHub{ReplayHub: hub.ReplayHub}
	if err := hub.Publish(t.Context(), api.EventTopicSettings, events.PublishKey{}, struct{}{}); err == nil {
		t.Fatal("expected publication failure")
	}
	if observer.calls != 1 {
		t.Fatal("failed publication reached preparation")
	}
}
