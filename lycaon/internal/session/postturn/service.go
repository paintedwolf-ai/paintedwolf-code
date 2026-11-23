package postturn

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(ctx context.Context, id string) (*api.Session, error)
}

type Transcript interface {
	GetMessages(ctx context.Context, id string) ([]api.Message, error)
}
type Grounding interface {
	AfterPrompt(context.Context, string, []string) error
}
type Guidance interface {
	Emit(context.Context, string, anchor.ID, anchor.Envelope)
	QueueAdvisories(context.Context, string, []guidance.GuidanceNudge)
	TakePolicy(context.Context, string) ([]api.Message, error)
}
type Progress interface {
	Get(context.Context, string) string
}
type Service struct {
	sessionStore     Sessions
	transcriptStore  Transcript
	grounding        Grounding
	guidance         Guidance
	progress         Progress
	coordinatorFrame inject.CoordinatorTurnFrameSource
}

func New(sessionStore Sessions, transcriptStore Transcript, guidance Guidance) *Service {
	return &Service{sessionStore: sessionStore, transcriptStore: transcriptStore, guidance: guidance}
}
func (m *Service) SetGrounding(grounding Grounding)                 { m.grounding = grounding }
func (m *Service) SetProgress(progress Progress)                    { m.progress = progress }
func (m *Service) SetFrame(frame inject.CoordinatorTurnFrameSource) { m.coordinatorFrame = frame }
