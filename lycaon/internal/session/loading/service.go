package loading

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/sandbox"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(ctx context.Context, id string) (*api.Session, error)
}

type Compaction interface {
	CompactionViewCurrent(ctx context.Context, sessionID string, coveredThroughOrd int64, coveredThroughID string, sourceSeq int64) (bool, error)
	GetCompactionView(ctx context.Context, sessionID string) (*store.CompactionView, bool, error)
}

type Turns interface {
	LatestTurnLoadReceipt(ctx context.Context, sessionID string) (store.TurnLoadReceipt, bool, error)
	ListTurnLoadReceiptsForTurns(ctx context.Context, openingMessageIDs []string) ([]store.TurnLoadReceipt, error)
	PutTurnLoadReceipt(ctx context.Context, r store.TurnLoadReceipt) (store.TurnLoadReceipt, error)
}
type Workflow interface {
	ResolvedRequest(ctx context.Context, sessionID string) workflowfacts.ResolvedWorkflowRequest
}
type Workers interface {
	Get(string) (*api.WorkerTask, bool)
}
type Models interface {
	ModelResident(context.Context, string, string) (bool, bool)
	PromptCachePolicy(string, string) (providerprofile.PromptCachePolicy, bool)
}
type SkillRoster func(context.Context, *api.Session, string, []string) ([]skills.Skill, []extpacks.Diagnostic)
type ImplementState func(context.Context, *api.Session) surface.ImplementSessionState

// Service selects additions to the standing tool and instruction surface.
type Service struct {
	sessionStore    Sessions
	compactionStore Compaction
	turnStore       Turns
	Ledger          *turnload.Ledger
	Workspace       *sessionscope.Service
	decider         decide.Decider
	router          *llm.StaticModelRouter
	models          Models
	cost            cost.CostTracker
	policy          func() toolpolicy.Engine
	skills          SkillRoster
	implement       ImplementState
	workflows       Workflow
	workerQueue     Workers
	skillBody       SkillBodyRenderer
	profiles        func(context.Context, *api.Session) []sandbox.ToolProfile
	events          *events.Publisher
}

func New(sessionStore Sessions, compactionStore Compaction, turnStore Turns, workspace *sessionscope.Service, router *llm.StaticModelRouter, models Models, cost cost.CostTracker, policy func() toolpolicy.Engine, skills SkillRoster, implement ImplementState) *Service {
	return &Service{sessionStore: sessionStore, compactionStore: compactionStore, turnStore: turnStore, Workspace: workspace, router: router, models: models, cost: cost, policy: policy, skills: skills, implement: implement}
}
func (m *Service) SetWorkflow(workflow Workflow)         { m.workflows = workflow }
func (m *Service) SetWorkers(workers Workers)            { m.workerQueue = workers }
func (m *Service) SetPublisher(events *events.Publisher) { m.events = events }
func (m *Service) SetProfiles(profiles func(context.Context, *api.Session) []sandbox.ToolProfile) {
	m.profiles = profiles
}

func (m *Service) SetPolicy(policy func() toolpolicy.Engine) { m.policy = policy }
