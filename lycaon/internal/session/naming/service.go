package naming

import (
	"context"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	SessionEventsOutboxed() bool
	Get(ctx context.Context, id string) (*api.Session, error)
	UpdateSession(ctx context.Context, id string, fn func(*api.Session)) error
	UpdateTitleIfUnset(ctx context.Context, id, title string) (bool, error)
}

type Transcript interface {
	LastTurnMessageContent(ctx context.Context, sessionID string) (string, error)
}

type Evidence interface {
	SessionUntrustedContent(sessionID string) bool
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
	sessionStore    Sessions
	transcriptStore Transcript
	evidenceStore   Evidence
	roots           Roots
	projects        Projects
	namers          Namers
	cost            cost.CostTracker
	publisher       *events.Publisher
	presence        Presence
}

func New(sessionStore Sessions, transcriptStore Transcript, evidenceStore Evidence, roots Roots, namers Namers, tracker cost.CostTracker) *Service {
	return &Service{sessionStore: sessionStore, transcriptStore: transcriptStore, evidenceStore: evidenceStore, roots: roots, namers: namers, cost: tracker}
}
func (s *Service) SetProjects(projects Projects)            { s.projects = projects }
func (s *Service) SetPublisher(publisher *events.Publisher) { s.publisher = publisher }
func (s *Service) SetPresence(presence Presence)            { s.presence = presence }
func (s *Service) lastMessage(ctx context.Context, sessionID string) string {
	content, err := s.transcriptStore.LastTurnMessageContent(ctx, sessionID)
	if err != nil {
		return ""
	}
	return content
}
