package capabilityadmin

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
)

// ChatGrantLedger is the durable record of chat-lifetime approvals.
type ChatGrantLedger interface {
	ForgetChatGrant(ctx context.Context, id string) (bool, error)
}

// Authority is the chat-scoped grant state and ledgers the capability routes
// list, install, and revoke. A nil runtime means the host did not build it.
type Authority struct {
	ApprovalDecisions ApprovalDecisionReader
	AuthzRecorder     authzledger.Recorder
	ChatGrants        ChatGrantLedger
	DirectIP          *approvalstate.DirectIPCapabilityRuntime
	GrantedPaths      *grantedpath.Runtime
	Listen            *approvalstate.SandboxPortGrantRuntime
	Loopback          *approvalstate.SandboxPortGrantRuntime
	ReadPaths         *approvalstate.SandboxPathGrantRuntime
	WriteRoots        *approvalstate.SandboxPathGrantRuntime
	Sockets           *approvalstate.SocketCapabilityRuntime
}

// Deps are the capability routes' dependencies, fixed at construction.
type Deps struct {
	HostResources *hostresources.Service
	Authority
	Gate           hitl.ApprovalGate
	Checkpoints    hitl.CheckpointManager
	Events         events.ReplayHub
	LLMService     *llm.Service
	ManagedSecrets *secretcap.Service
	Projects       project.Registry
	Store          session.Store
	Settings       *settings.Service
}

type Handler struct {
	Deps
	// options resolves a tool approval by the option the person chose, atomically.
	authorityMu sync.Mutex
	options     hitl.ApprovalOptionResolver
	responses   *httpio.Responder
}

func New(responses *httpio.Responder, deps Deps) Handler {
	options, _ := deps.Checkpoints.(hitl.ApprovalOptionResolver)
	httpio.RequireDependencies("capabilityadmin",
		httpio.Required{Name: "responses", Present: responses != nil},
		httpio.Required{Name: "Checkpoints", Present: options != nil},
		httpio.Required{Name: "Projects", Present: deps.Projects != nil},
		httpio.Required{Name: "Store", Present: deps.Store != nil},
		httpio.Required{Name: "ApprovalDecisions", Present: deps.ApprovalDecisions != nil},
		httpio.Required{Name: "Gate", Present: deps.Gate != nil},
		httpio.Required{Name: "Settings.Approvals", Present: deps.Settings != nil && deps.Settings.Approvals != nil},
	)
	return Handler{Deps: deps, options: options, responses: responses}
}
