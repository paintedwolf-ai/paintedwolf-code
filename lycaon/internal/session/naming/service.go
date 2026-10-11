package naming

import (
	"context"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/pkg/api"
)

type Store interface {
	Get(context.Context, string) (*api.Session, error)
	UpdateSession(context.Context, string, func(*api.Session)) error
	UpdateTitleIfUnset(context.Context, string, string) (bool, error)
	MutationEventsOutboxed() bool
	LastTurnMessageContent(context.Context, string) (string, error)
	SessionUntrustedContent(string) bool
}

type Projects interface {
	Get(context.Context, string) (*project.Project, error)
	UpdateNameIfUnset(context.Context, string, string) (bool, error)
	MutationEventsOutboxed() bool
}

type Roots interface {
	SettingsPath(context.Context, *api.Session) string
}
type Namers interface {
	Namer(string, string, string, string) llm.UtilityNamer
}
type Presence interface {
	ObserveSession(context.Context, api.SessionEvent)
}

type Service struct {
	store     Store
	roots     Roots
	projects  Projects
	namers    Namers
	cost      cost.CostTracker
	publisher *events.Publisher
	presence  Presence
}

func New(store Store, roots Roots, namers Namers, tracker cost.CostTracker) *Service {
	return &Service{store: store, roots: roots, namers: namers, cost: tracker}
}
func (s *Service) SetProjects(projects Projects)            { s.projects = projects }
func (s *Service) SetPublisher(publisher *events.Publisher) { s.publisher = publisher }
func (s *Service) SetPresence(presence Presence)            { s.presence = presence }
func (s *Service) lastMessage(ctx context.Context, sessionID string) string {
	content, err := s.store.LastTurnMessageContent(ctx, sessionID)
	if err != nil {
		return ""
	}
	return content
}
