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
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(context.Context, string) (*api.Session, error)
	GetMessages(context.Context, string) ([]api.Message, error)
	GetMessage(context.Context, string, string) (api.Message, error)
}
type Transcript interface {
	Append(context.Context, string, ...api.Message) error
}
type CycleState interface {
	ForSession(context.Context, *api.Session) surface.ImplementSessionState
}
type BudgetRequests interface{ BudgetRequestOpen(string) bool }
type WorkflowDomains struct {
	Policy WorkflowPolicy
	Runs   WorkflowRuns
}
type WorkflowPolicy interface {
	ActiveReviewVerdictPending(ctx context.Context, sessionID string) bool
	ScaffoldVarsForSession(ctx context.Context, sessionID string) (map[string]any, error)
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
	store        Sessions
	transcript   Transcript
	state        CycleState
	budgets      BudgetRequests
	workers      workeroutcomes.CycleLedger
	workflows    *WorkflowDomains
	frame        inject.CoordinatorTurnFrameSource
	progress     Progress
	closure      Closure
	gateFeedback *feedback.GateFeedbackCatalog
	rejectFmt    *guidance.StaticRejectFormatter
	renderer     *oar.Renderer
	kicks        *kick.KickEngine
	anchors      *anchor.Bus
}

func New(sessions Sessions, transcript Transcript, state CycleState, budgets BudgetRequests, closure Closure) *Service {
	return &Service{store: sessions, transcript: transcript, state: state, budgets: budgets, closure: closure}
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
