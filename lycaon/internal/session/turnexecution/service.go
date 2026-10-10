package turnexecution

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/authorization"
	"github.com/lycaon/lycaon/internal/session/closeouts"
	"github.com/lycaon/lycaon/internal/session/curation"
	"github.com/lycaon/lycaon/internal/session/execution"
	"github.com/lycaon/lycaon/internal/session/history"
	"github.com/lycaon/lycaon/internal/session/instructions"
	"github.com/lycaon/lycaon/internal/session/postturn"
	"github.com/lycaon/lycaon/internal/session/preparation"
	"github.com/lycaon/lycaon/internal/session/spendguard"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/submissionstate"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/internal/session/turnclock"
	"github.com/lycaon/lycaon/internal/session/turnsettlement"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(context.Context, string) (*api.Session, error)
	SetSessionStatus(context.Context, string, api.SessionStatus) error
	CheckpointTurn(context.Context, string, string, store.TurnPhase, string) error
	SealTurnCloseout(context.Context, store.TurnCloseoutCommit) error
}
type Workspace interface {
	SettingsRoots(context.Context, *api.Session) []string
}
type Chats interface {
	UnarchiveOnPrompt(context.Context, *api.Session) error
}
type Guidance interface {
	PendingIDs(context.Context, string) []string
	Emit(context.Context, string, anchor.ID, anchor.Envelope)
}
type Grounding interface{ IsEscalated(string) bool }
type SlashCommands interface {
	TrySlashPrompt(context.Context, string, string, string) (*promptresult.Result, bool, error)
}
type Requests interface {
	AcceptsEmptyRequest(context.Context, string) bool
	PrepareUserRequest(context.Context, string, string) (string, *promptresult.Result, bool, error)
}
type RunControl interface {
	AssertSessionRunnable(context.Context, string) error
}
type Service struct {
	Execution       *execution.Lifetime
	Workspace       Workspace
	Spend           *spendguard.Service
	Transcript      *transcript.Service
	Turns           *execution.Journal
	Chats           Chats
	SubmissionState *submissionstate.Service
	Settlement      *turnsettlement.Service
	Clocks          *turnclock.Service
	Closeouts       *closeouts.Service
	Guidance        Guidance
	Curation        *curation.Service
	Status          *execution.Status
	Instructions    *instructions.Service
	Preparation     *preparation.Service
	Authorization   *authorization.Service
	History         *history.Service
	PostTurn        *postturn.Service
	store           Sessions
	llmSvc          *llm.Service
	grounding       Grounding
	workflow        RunControl
	requests        Requests
	slash           SlashCommands
	Coordinator     *coordinator.Runtime
}
type Components struct {
	Execution       *execution.Lifetime
	Workspace       Workspace
	Spend           *spendguard.Service
	Transcript      *transcript.Service
	Turns           *execution.Journal
	Chats           Chats
	SubmissionState *submissionstate.Service
	Settlement      *turnsettlement.Service
	Clocks          *turnclock.Service
	Closeouts       *closeouts.Service
	Guidance        Guidance
	Curation        *curation.Service
	Status          *execution.Status
	Instructions    *instructions.Service
	Preparation     *preparation.Service
	Authorization   *authorization.Service
	History         *history.Service
	PostTurn        *postturn.Service
}

func New(store Sessions, llmSvc *llm.Service, c Components) *Service {
	return &Service{store: store, llmSvc: llmSvc, Execution: c.Execution, Workspace: c.Workspace, Spend: c.Spend, Transcript: c.Transcript, Turns: c.Turns, Chats: c.Chats, SubmissionState: c.SubmissionState, Settlement: c.Settlement, Clocks: c.Clocks, Closeouts: c.Closeouts, Guidance: c.Guidance, Curation: c.Curation, Status: c.Status, Instructions: c.Instructions, Preparation: c.Preparation, Authorization: c.Authorization, History: c.History, PostTurn: c.PostTurn}
}
func (m *Service) SetRuntime(runtime *coordinator.Runtime) { m.Coordinator = runtime }
func (m *Service) SetGrounding(grounding Grounding)        { m.grounding = grounding }
func (m *Service) SetControl(workflow RunControl)          { m.workflow = workflow }
func (m *Service) SetRequests(requests Requests)           { m.requests = requests }
func (m *Service) SetSlash(slash SlashCommands)            { m.slash = slash }
func (m *Service) assertRunnable(ctx context.Context, id string) error {
	if m.workflow == nil {
		return nil
	}
	return m.workflow.AssertSessionRunnable(ctx, id)
}

func (m *Service) AcceptsEmpty(ctx context.Context, id string) bool {
	return m != nil && m.requests != nil && m.requests.AcceptsEmptyRequest(ctx, id)
}
