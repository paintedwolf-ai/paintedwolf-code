package workerexecution

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workerharness"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/session/workerresults"
	"github.com/lycaon/lycaon/internal/session/workerworkspace"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(context.Context, string) (*api.Session, error)
	CreateChild(context.Context, *api.Session, api.SpawnChildRequest) (*api.Session, error)
	Delete(context.Context, string) error
	UpdateSession(context.Context, string, func(*api.Session)) error
	SessionUntrustedContentResult(context.Context, string) (bool, error)
	SessionSecretExposure(context.Context, string) (bool, error)
	SeedUntrustedContent(context.Context, string) error
	SeedSecretExposure(context.Context, string) error
	LoadLedger(context.Context, string) (evidence.Ledger, error)
}
type Workspace interface {
	SettingsRoots(context.Context, *api.Session) []string
}
type Admission interface {
	WithSessionTreeAdmission(context.Context, string, func() error) error
}
type Guidance interface {
	EmitEager(context.Context, string, anchor.ID, map[string]string)
}
type Limits interface {
	Compaction(context.Context, *api.Session) compaction.CompactionConfig
}
type History interface{ WorkerGroundingRetries() int }
type Decisions interface {
	Get(context.Context, string) (api.WorkerDecisionRequest, bool, error)
}
type Service struct {
	Harness       *workerharness.Service
	Workspaces    *workerworkspace.Service
	Cancel        *workerresults.GracefulCancel
	Cards         *workerresults.Cards
	Cancellations *workeroutcomes.Cancellations
	Delivery      *workeroutcomes.DeliveryPolicy
	Summaries     *workeroutcomes.Summaries
	Results       *workeroutcomes.Results
	Digests       *workeroutcomes.Digests
	State         *workeroutcomes.State
	Notes         *workeroutcomes.Notes
	Rejections    *workeroutcomes.PeerRejectionFeed

	store          Sessions
	workspace      Workspace
	gate           Admission
	guidance       Guidance
	router         *llm.StaticModelRouter
	limits         Limits
	history        History
	prompts        prompts.PromptTemplateEngine
	workerContext  assembly.WorkerContextBuilder
	workflowHints  *guidance.HintConfig
	workspaceCheck workercompletion.WorkspaceChangeChecker
	pipeline       *oar.GuardPipeline
	decisions      Decisions
}

func New(sessions Sessions, workspace Workspace, gate Admission, guidance Guidance, limits Limits, history History, router *llm.StaticModelRouter, peers PeerServices) *Service {
	return &Service{Workspaces: peers.Workspaces, Cancel: peers.Cancel, Cards: peers.Cards, Cancellations: peers.Cancellations, Delivery: peers.Delivery, Summaries: peers.Summaries, Results: peers.Results, Digests: peers.Digests, State: peers.State, Notes: peers.Notes, Rejections: peers.Rejections, store: sessions, workspace: workspace, gate: gate, guidance: guidance, limits: limits, history: history, router: router}
}
func (m *Service) SessionByID(ctx context.Context, id string) (*api.Session, error) {
	return m.store.Get(ctx, id)
}
func (m *Service) SetPromptEngine(engine prompts.PromptTemplateEngine) { m.prompts = engine }
func (m *Service) SetContext(context assembly.WorkerContextBuilder)    { m.workerContext = context }
func (m *Service) SetDecisions(decisions Decisions)                    { m.decisions = decisions }
func (m *Service) SetEvaluation(check workercompletion.WorkspaceChangeChecker, hints *guidance.HintConfig, pipeline *oar.GuardPipeline) {
	m.workspaceCheck = check
	m.workflowHints = hints
	m.pipeline = pipeline
}

type PeerServices struct {
	Workspaces    *workerworkspace.Service
	Cancel        *workerresults.GracefulCancel
	Cards         *workerresults.Cards
	Cancellations *workeroutcomes.Cancellations
	Delivery      *workeroutcomes.DeliveryPolicy
	Summaries     *workeroutcomes.Summaries
	Results       *workeroutcomes.Results
	Digests       *workeroutcomes.Digests
	State         *workeroutcomes.State
	Notes         *workeroutcomes.Notes
	Rejections    *workeroutcomes.PeerRejectionFeed
}
