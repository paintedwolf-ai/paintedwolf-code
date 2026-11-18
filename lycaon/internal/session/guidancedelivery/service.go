package guidancedelivery

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(ctx context.Context, id string) (*api.Session, error)
}

type TranscriptStore interface {
	GetMessage(ctx context.Context, sessionID, messageID string) (api.Message, error)
	GetMessages(ctx context.Context, id string) ([]api.Message, error)
}
type Transcript interface {
	Append(context.Context, string, ...api.Message) error
}
type CycleState interface {
	ForSession(context.Context, *api.Session) surface.ImplementSessionState
}
type RoundNotices interface{ RoundRunning(string) bool }
type WorkflowDomains struct {
	Policy WorkflowPolicy
	Runs   WorkflowRuns
}
type WorkflowPolicy interface {
	ActiveReviewVerdictPending(ctx context.Context, sessionID string) bool
	ScaffoldVarsForSession(ctx context.Context, sessionID string) (map[string]any, error)
	ActiveManifest(ctx context.Context, sessionID string) (workflowfacts.ActiveWorkflowManifest, bool)
}
type WorkflowRuns interface {
	ActiveBySession(context.Context, string) (*api.WorkflowRun, error)
}

type Progress interface {
	Get(context.Context, string) string
}
type Closure interface {
	Baseline(string) (guard.ProgressClosureBaseline, bool)
}
type Service struct {
	sessionStore    Sessions
	transcriptStore TranscriptStore
	transcript      Transcript
	state           CycleState
	rounds          RoundNotices
	workers         workeroutcomes.CycleLedger
	workflows       *WorkflowDomains
	frame           inject.CoordinatorTurnFrameSource
	progress        Progress
	closure         Closure
	gateFeedback    *feedback.GateFeedbackCatalog
	rejectFmt       *guidance.StaticRejectFormatter
	renderer        *oar.Renderer
	kicks           *kick.KickEngine
	anchors         *anchor.Bus
}

func New(sessionStore Sessions, transcriptStore TranscriptStore, transcript Transcript, state CycleState, rounds RoundNotices, closure Closure) *Service {
	return &Service{sessionStore: sessionStore, transcriptStore: transcriptStore, transcript: transcript, state: state, rounds: rounds, closure: closure}
}
func (m *Service) Bind(kicks *kick.KickEngine, anchors *anchor.Bus) {
	m.kicks = kicks
	m.anchors = anchors
}
func (m *Service) SetWorkers(workers workeroutcomes.CycleLedger)     { m.workers = workers }
func (m *Service) SetWorkflow(workflows *WorkflowDomains)            { m.workflows = workflows }
func (m *Service) SetFrame(frame inject.CoordinatorTurnFrameSource)  { m.frame = frame }
func (m *Service) SetProgress(progress Progress)                     { m.progress = progress }
func (m *Service) SetFeedback(catalog *feedback.GateFeedbackCatalog) { m.gateFeedback = catalog }
func (m *Service) SetRejectFormatter(formatter *guidance.StaticRejectFormatter) {
	m.rejectFmt = formatter
}
func (m *Service) SetRenderer(renderer *oar.Renderer) { m.renderer = renderer }
