package closeoutassembly

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(context.Context, string) (*api.Session, error)
	GetMessages(context.Context, string) ([]api.Message, error)
	LoadLedger(context.Context, string) (evidence.Ledger, error)
}
type Workspace interface {
	Roots(context.Context, *api.Session) ([]projectroot.RootRef, error)
}
type State interface {
	ForSession(context.Context, *api.Session) surface.ImplementSessionState
}
type Evidence interface {
	guidance.CloseoutEvidenceReader
	Tasks(context.Context, string, time.Time) ([]api.WorkerTask, error)
}
type WorkflowDomains struct {
	Policy WorkflowPolicy
	Runs   WorkflowRuns
}
type WorkflowPolicy interface {
	CurrentPhase(ctx context.Context, sessionID string) string
	ScaffoldVarsForSession(ctx context.Context, sessionID string) (map[string]any, error)
}
type WorkflowRuns interface {
	ActiveBySession(context.Context, string) (*api.WorkflowRun, error)
}

type Delegations interface {
	DelegationBySessionID(string) (string, bool)
	ListLegs(context.Context, string) ([]api.Leg, error)
}
type Service struct {
	store            Sessions
	workspace        Workspace
	state            State
	evidence         Evidence
	workflows        *WorkflowDomains
	delegations      Delegations
	synthesisCurator llm.Curator
	prompts          prompts.PromptTemplateEngine
}

func New(store Sessions, workspace Workspace, state State, evidence Evidence) *Service {
	return &Service{store: store, workspace: workspace, state: state, evidence: evidence}
}
func (s *Service) SetWorkflow(workflows *WorkflowDomains)          { s.workflows = workflows }
func (s *Service) SetDelegations(delegations Delegations)          { s.delegations = delegations }
func (s *Service) SetPrompts(prompts prompts.PromptTemplateEngine) { s.prompts = prompts }
