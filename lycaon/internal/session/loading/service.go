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
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/pkg/api"
)

type Store interface {
	Get(context.Context, string) (*api.Session, error)
	LatestTurnLoadReceipt(context.Context, string) (store.TurnLoadReceipt, bool, error)
	PutTurnLoadReceipt(context.Context, store.TurnLoadReceipt) (store.TurnLoadReceipt, error)
	ListTurnLoadReceiptsForTurns(context.Context, []string) ([]store.TurnLoadReceipt, error)
	GetCompactionView(context.Context, string) (*store.CompactionView, bool, error)
	CompactionViewCurrent(context.Context, string, int64, string, int64) (bool, error)
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
	store       Store
	Ledger      *turnload.Ledger
	Workspace   *sessionscope.Service
	decider     decide.Decider
	router      *llm.StaticModelRouter
	models      Models
	cost        cost.CostTracker
	policy      func() toolpolicy.Engine
	skills      SkillRoster
	implement   ImplementState
	workflows   Workflow
	workerQueue Workers
	skillBody   SkillBodyRenderer
	events      *events.Publisher
}

func New(store Store, workspace *sessionscope.Service, router *llm.StaticModelRouter, models Models, cost cost.CostTracker, policy func() toolpolicy.Engine, skills SkillRoster, implement ImplementState) *Service {
	return &Service{store: store, Workspace: workspace, router: router, models: models, cost: cost, policy: policy, skills: skills, implement: implement}
}
func (m *Service) SetWorkflow(workflow Workflow)         { m.workflows = workflow }
func (m *Service) SetWorkers(workers Workers)            { m.workerQueue = workers }
func (m *Service) SetPublisher(events *events.Publisher) { m.events = events }

func (m *Service) SetPolicy(policy func() toolpolicy.Engine) { m.policy = policy }
