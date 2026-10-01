// Package events defines the in-process event hub.
package events

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/pkg/api"
)

var ErrReplayUnavailable = errors.New("event replay unavailable")

const (
	// HeartbeatInterval is the SSE idle ping interval.
	HeartbeatInterval = 20 * time.Second

	// SubscriberBufferSize is the per-subscriber event channel capacity.
	SubscriberBufferSize = 256

	DebounceBoard   = 200 * time.Millisecond
	DebounceSession = 80 * time.Millisecond
	DebounceCost    = 200 * time.Millisecond
	DebounceLLM     = 80 * time.Millisecond
	// DebounceMessagePatch coalesces `message` topic patches for a settled row.
	DebounceMessagePatch = 48 * time.Millisecond
	// DebounceLiveProjection controls the independent live-draft flush.
	DebounceLiveProjection = 48 * time.Millisecond
	// DebounceSourceChanged coalesces equivalent path facets.
	DebounceSourceChanged = 150 * time.Millisecond
	DebounceImmediate     = 0
)

// DebounceForTopic returns the trailing-edge debounce window for a topic.
func DebounceForTopic(topic api.EventTopic) time.Duration {
	switch topic {
	case api.EventTopicBoard:
		return DebounceBoard
	case api.EventTopicSession:
		return DebounceSession
	case api.EventTopicCost:
		return DebounceCost
	case api.EventTopicLLM:
		return DebounceLLM
	case api.EventTopicProviders, api.EventTopicModelPolicy, api.EventTopicSettings:
		return DebounceLLM
	// Transition events must preserve every state change.
	// Agent presence is too: a trailing window would drop a short call's start
	// and hold delivery while calls keep arriving.
	case api.EventTopicMessage, api.EventTopicActivity, api.EventTopicWorkflow, api.EventTopicGrounding,
		api.EventTopicFindings, api.EventTopicProgress, api.EventTopicTurnClock, api.EventTopicAgentPresence:
		return DebounceImmediate
	case api.EventTopicSourceChanged:
		return DebounceSourceChanged
	default:
		return DebounceImmediate
	}
}

// PublishKey scopes an event's delivery and its debounce coalescing.
type PublishKey struct {
	Project string
	Session string
	// EventID is stable across durable outbox delivery retries.
	EventID string
	// EntityRevision lets projections reject stale updates.
	EntityRevision uint64
	// Facet prevents unrelated updates on one topic from coalescing.
	Facet string
}

func (k PublishKey) Scope() api.EventScope {
	switch {
	case k.Project != "" && k.Session != "":
		return api.EventScope{Kind: api.EventScopeSession, ProjectID: k.Project, SessionID: k.Session}
	case k.Project != "":
		return api.EventScope{Kind: api.EventScopeProject, ProjectID: k.Project}
	default:
		return api.EventScope{Kind: api.EventScopeDevice}
	}
}

// ValidatePublishScope rejects a key that lacks the scope its topic requires.
// The durable outbox applies it at insert time as well as publish time.
func ValidatePublishScope(topic api.EventTopic, key PublishKey) error {
	if key.Project != "" {
		if err := uuid.Validate(key.Project); err != nil {
			return fmt.Errorf("event topic %q requires a project UUID: %w", topic, err)
		}
		return nil
	}
	switch topic {
	case api.EventTopicAttention, api.EventTopicCLIOpen, api.EventTopicPreflight,
		api.EventTopicSettings, api.EventTopicProviders, api.EventTopicModelPolicy:
		if key.Session == "" {
			return nil
		}
	default:
	}
	return fmt.Errorf("event topic %q requires project scope", topic)
}

// ErrSubscriptionViewer rejects a subscription with no authenticated person.
var ErrSubscriptionViewer = errors.New("event subscription requires a viewer")

// Subscription selects the events one stream delivers.
type Subscription struct {
	// Project narrows project content; empty receives every project.
	Project string
	// Viewer is the person the stream delivers to.
	Viewer people.Person
	// After resumes after a replay cursor; empty starts from now.
	After string
}

// EventHub publishes domain events to SSE subscribers.
type EventHub interface {
	Publish(ctx context.Context, topic api.EventTopic, key PublishKey, data any) error
	// Subscribe registers a stream and queues retained events after sub.After
	// atomically. A cursor outside the retained window is ErrReplayUnavailable.
	Subscribe(ctx context.Context, sub Subscription) (<-chan api.EventEnvelope, func(), error)
	// SubscriberCount reports live subscribers. Presence gates unattended
	// background work on it: zero means no client is watching.
	SubscriberCount() int
}

// ReplayHub exposes the replay boundary a client resumes from.
type ReplayHub interface {
	EventHub
	CurrentCursor() string
}
