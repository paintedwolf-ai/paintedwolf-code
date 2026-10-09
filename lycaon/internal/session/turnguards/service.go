package turnguards

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/session/batchcontrol"
	"github.com/lycaon/lycaon/internal/session/closeouts"
	sessionlimits "github.com/lycaon/lycaon/internal/session/limits"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	"github.com/lycaon/lycaon/internal/session/policyfeedback"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/session/progressclosure"
	sessionverification "github.com/lycaon/lycaon/internal/session/verification"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(context.Context, string) (*api.Session, error)
}
type Progress interface {
	Get(context.Context, string) string
}
type State interface {
	ForSession(context.Context, *api.Session) surface.ImplementSessionState
}
type Decisions interface {
	DecisionPending(context.Context, string) bool
}
type Surface interface{ PromptTurnSurfaceID(string) string }
type Workspace interface {
	ActivePath(context.Context, *api.Session) (string, error)
	SettingsPath(context.Context, *api.Session) string
	RootCount(context.Context, *api.Session) int
	SettingsRoots(context.Context, *api.Session) []string
}
type Workflow interface {
	toolpolicy.WorkflowView
	ActivePhaseGuardState(context.Context, string) workflowfacts.WorkflowPhaseGuardState
	ActiveCloseoutGateState(context.Context, string) workflowfacts.WorkflowCloseoutGateState
	ActiveReviewVerdictPending(context.Context, string) bool
	ParallelTaskMaxWorkers(context.Context, string) int
	ParallelTaskMaxReadWorkers(context.Context, string) int
	ParallelTaskMaxWriteWorkers(context.Context, string) int
}
type Service struct {
	store               Sessions
	progress            Progress
	closeouts           *closeouts.Service
	ToolPolicy          *policyfacts.Service
	Verification        *sessionverification.Service
	Feedback            *policyfeedback.Service
	state               State
	decisions           Decisions
	Workspace           Workspace
	Batch               *batchcontrol.Service
	ProgressClosure     *progressclosure.Service
	surface             Surface
	workflows           Workflow
	workspaceCheck      workercompletion.WorkspaceChangeChecker
	repoProvider        repoinfo.Provider
	toolInvoker         tools.ToolInvoker
	rules               toolpolicy.RuleEvaluator
	Profiles            *profiles.Service
	Limits              *sessionlimits.Service
	toolRejectFormatter *guidance.ToolRejectFormatter
	profileRuntimeRules *toolpolicy.ProfileRuntimeRules
	Rejects             *guidance.StaticRejectFormatter
	coordinatorFrame    inject.CoordinatorTurnFrameSource
	workerQueue         workeroutcomes.CycleLedger
}

func New(store Sessions, policy *policyfacts.Service, verification *sessionverification.Service, feedback *policyfeedback.Service, state State, decisions Decisions, workspace Workspace, batch *batchcontrol.Service, closure *progressclosure.Service, closeouts *closeouts.Service, profiles *profiles.Service, limits *sessionlimits.Service) *Service {
	return &Service{store: store, ToolPolicy: policy, Verification: verification, Feedback: feedback, state: state, decisions: decisions, Workspace: workspace, Batch: batch, ProgressClosure: closure, closeouts: closeouts, Profiles: profiles, Limits: limits}
}
func (m *Service) SetSurface(surface Surface)                    { m.surface = surface }
func (m *Service) SetWorkflow(workflow Workflow)                 { m.workflows = workflow }
func (m *Service) SetWorkers(workers workeroutcomes.CycleLedger) { m.workerQueue = workers }
func (m *Service) SetProgress(progress Progress)                 { m.progress = progress }
func (m *Service) SetWorkspaceCheck(check workercompletion.WorkspaceChangeChecker) {
	m.workspaceCheck = check
}
func (m *Service) SetRepoProvider(provider repoinfo.Provider) { m.repoProvider = provider }
func (m *Service) SetInvoker(invoker tools.ToolInvoker)       { m.toolInvoker = invoker }
func (m *Service) SetRules(rules toolpolicy.RuleEvaluator)    { m.rules = rules }
func (m *Service) SetRejects(formatter *guidance.StaticRejectFormatter, toolFormatter *guidance.ToolRejectFormatter) {
	m.Rejects = formatter
	m.toolRejectFormatter = toolFormatter
}
func (m *Service) SetRuntimeRules(rules *toolpolicy.ProfileRuntimeRules) {
	m.profileRuntimeRules = rules
}
func (m *Service) SetFrame(frame inject.CoordinatorTurnFrameSource) { m.coordinatorFrame = frame }
func (m *Service) Policy() toolpolicy.Engine                        { return toolpolicy.NewEngine(m.PolicyDependencies()) }
