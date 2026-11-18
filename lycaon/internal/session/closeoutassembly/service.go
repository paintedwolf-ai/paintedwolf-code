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
	Get(ctx context.Context, id string) (*api.Session, error)
}

type Transcript interface {
	GetMessages(ctx context.Context, id string) ([]api.Message, error)
}

type EvidenceStore interface {
	LoadLedger(ctx context.Context, sessionID string) (evidence.Ledger, error)
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

type Service struct {
	sessionStore       Sessions
	transcriptStore    Transcript
	evidenceStore      EvidenceStore
	workspace          Workspace
	state              State
	evidence           Evidence
	workflows          *WorkflowDomains
	synthesisCurator   llm.Curator
	prompts            prompts.PromptTemplateEngine
	sourceVerification SourceVerification
}

func New(sessionStore Sessions, transcriptStore Transcript, evidenceStore EvidenceStore, workspace Workspace, state State, evidence Evidence) *Service {
	return &Service{sessionStore: sessionStore, transcriptStore: transcriptStore, evidenceStore: evidenceStore, workspace: workspace, state: state, evidence: evidence}
}
func (s *Service) SetWorkflow(workflows *WorkflowDomains)          { s.workflows = workflows }
func (s *Service) SetPrompts(prompts prompts.PromptTemplateEngine) { s.prompts = prompts }
