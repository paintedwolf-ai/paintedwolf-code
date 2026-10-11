package workerharness

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	CommitEvidenceToolResult(context.Context, string, string, string, map[string]any, string) (string, string, error)
	AppendMessages(context.Context, string, ...api.Message) error
}
type Profiles interface {
	PromptToolProfile(context.Context, *api.Session) (string, error)
}
type Context interface {
	Build(context.Context, *api.Session, string, inject.Machine) (tools.ToolContext, error)
}
type Workspaces interface {
	Enrich(context.Context, *api.Session, tools.ToolContext) (tools.ToolContext, error)
}
type Workspace interface {
	ActivePath(context.Context, *api.Session) (string, error)
}
type Tools interface {
	Run(context.Context, string, map[string]any, tools.ToolContext) (string, error)
}
type Verification interface {
	RecordSourceRunEvidence(context.Context, string, *api.Session, string, tools.SourceRunCapture)
	WorkerSourceRuns(context.Context, string) ([]workercompletion.WorkerInvocationReceipt, error)
	SourceVerifyCommand(context.Context, string) string
	WorkerRevision(context.Context, *api.WorkerTask) workercompletion.SourceRevision
}
type SourceProof func(context.Context, *api.WorkerTask, []api.Message, string, workercompletion.SourceRevision) workercompletion.WorkerCompletionProof

type Service struct {
	store        Sessions
	Profiles     Profiles
	Context      Context
	Workspaces   Workspaces
	Workspace    Workspace
	tools        Tools
	Verification Verification
	proof        SourceProof
}

func New(sessions Sessions, profiles Profiles, context Context, workspaces Workspaces, workspace Workspace, tools Tools, verification Verification, proof SourceProof) *Service {
	return &Service{store: sessions, Profiles: profiles, Context: context, Workspaces: workspaces, Workspace: workspace, tools: tools, Verification: verification, proof: proof}
}
