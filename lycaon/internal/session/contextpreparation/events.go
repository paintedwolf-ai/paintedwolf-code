package contextpreparation

import (
	"encoding/json"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/pkg/api"
)

// Observe receives committed publications without becoming a presence subscriber.
func (s *Service) Observe(topic api.EventTopic, key events.PublishKey, data any) {
	if s == nil {
		return
	}
	switch topic {
	case api.EventTopicMessage, api.EventTopicWorkflow, api.EventTopicSourceChanged,
		api.EventTopicSettings, api.EventTopicProviders, api.EventTopicModelPolicy:
		s.Changed(key.Session, key.Project)
	case api.EventTopicSession:
		var event api.SessionEvent
		switch value := data.(type) {
		case api.SessionEvent:
			event = value
		case *api.SessionEvent:
			if value == nil {
				return
			}
			event = *value
		default:
			encoded, err := json.Marshal(data)
			if err != nil || json.Unmarshal(encoded, &event) != nil {
				return
			}
		}
		if event.Action == api.SessionEventActionDeleted || event.Status != api.SessionStatusIdle || event.PromptPending {
			s.Suspend(event.ID, event.Action == api.SessionEventActionDeleted)
		} else {
			s.Retry(event.ID)
		}
	}
}
