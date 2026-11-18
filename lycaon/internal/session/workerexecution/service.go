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
	CreateChild(ctx context.Context, parent *api.Session, req api.SpawnChildRequest) (*api.Session, error)
	Delete(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (*api.Session, error)
	UpdateSession(ctx context.Context, id string, fn func(*api.Session)) error
}

type Evidence interface {
	LoadLedger(ctx context.Context, sessionID string) (evidence.Ledger, error)
	SeedSecretExposure(ctx context.Context, sessionID string) error
	SeedUntrustedContent(ctx context.Context, sessionID string) error
	SessionSecretExposure(ctx context.Context, sessionID string) (bool, error)
	SessionUntrustedContentResult(ctx context.Context, sessionID string) (bool, error)
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

	sessionStore   Sessions
	evidenceStore  Evidence
	workspace      Workspace
	gate           Admission
	guidance       Guidance
	router         *llm.StaticModelRouter
	limits         Limits
	prompts        prompts.PromptTemplateEngine
	workerContext  assembly.WorkerContextBuilder
	workflowHints  *guidance.HintConfig
	WorkspaceCheck workercompletion.WorkspaceChangeChecker
	pipeline       *oar.GuardPipeline
	decisions      Decisions
}

func New(sessionStore Sessions, evidenceStore Evidence, workspace Workspace, gate Admission, guidance Guidance, limits Limits, router *llm.StaticModelRouter, peers PeerServices) *Service {
	return &Service{Workspaces: peers.Workspaces, Cancel: peers.Cancel, Cards: peers.Cards, Cancellations: peers.Cancellations, Delivery: peers.Delivery, Summaries: peers.Summaries, Results: peers.Results, Digests: peers.Digests, State: peers.State, Notes: peers.Notes, Rejections: peers.Rejections, sessionStore: sessionStore, evidenceStore: evidenceStore, workspace: workspace, gate: gate, guidance: guidance, limits: limits, router: router}
}
func (m *Service) SessionByID(ctx context.Context, id string) (*api.Session, error) {
	return m.sessionStore.Get(ctx, id)
}
func (m *Service) SetPromptEngine(engine prompts.PromptTemplateEngine) { m.prompts = engine }
func (m *Service) SetContext(context assembly.WorkerContextBuilder)    { m.workerContext = context }
func (m *Service) SetDecisions(decisions Decisions)                    { m.decisions = decisions }
func (m *Service) SetEvaluation(check workercompletion.WorkspaceChangeChecker, hints *guidance.HintConfig, pipeline *oar.GuardPipeline) {
	m.WorkspaceCheck = check
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
