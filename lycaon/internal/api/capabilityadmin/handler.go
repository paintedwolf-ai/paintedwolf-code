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
	"github.com/lycaon/lycaon/internal/presence"
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
	// Vault holds each chat's unlock for values a person stored.
	Vault *presence.Unlocks
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
	Access            *Access
	CheckpointActions *CheckpointActions
	Grants            *Grants
	Installation      *Installation
	Inventory         *Inventory
	HeldValues        *HeldValues
}

type Access struct {
	Events    events.ReplayHub
	Gate      hitl.ApprovalGate
	Grants    *Grants
	Inventory *Inventory
	Projects  project.Registry
	Settings  *settings.Service
	Store     session.Store
	responses *httpio.Responder
}

type CheckpointActions struct {
	Checkpoints    hitl.CheckpointManager
	Events         events.ReplayHub
	ManagedSecrets *secretcap.Service
	Projects       project.Registry
	Store          session.Store
	options        hitl.ApprovalOptionResolver
	presence       *hitl.VaultPresence
	responses      *httpio.Responder
}

type Grants struct {
	AuthzRecorder authzledger.Recorder
	ChatGrants    ChatGrantLedger
	Events        events.ReplayHub
	Gate          hitl.ApprovalGate
	GrantedPaths  *grantedpath.Runtime
	Inventory     *Inventory
	Listen        *approvalstate.SandboxPortGrantRuntime
	Loopback      *approvalstate.SandboxPortGrantRuntime
	Projects      project.Registry
	ReadPaths     *approvalstate.SandboxPathGrantRuntime
	Settings      *settings.Service
	Sockets       *approvalstate.SocketCapabilityRuntime
	WriteRoots    *approvalstate.SandboxPathGrantRuntime
	authorityMu   *sync.Mutex
	responses     *httpio.Responder
}

type Installation struct {
	DirectIP     *approvalstate.DirectIPCapabilityRuntime
	Gate         hitl.ApprovalGate
	GrantedPaths *grantedpath.Runtime
	LLMService   *llm.Service
	Listen       *approvalstate.SandboxPortGrantRuntime
	Loopback     *approvalstate.SandboxPortGrantRuntime
	ReadPaths    *approvalstate.SandboxPathGrantRuntime
	Sockets      *approvalstate.SocketCapabilityRuntime
	WriteRoots   *approvalstate.SandboxPathGrantRuntime
	authorityMu  *sync.Mutex
}

type Inventory struct {
	ApprovalDecisions ApprovalDecisionReader
	AuthzRecorder     authzledger.Recorder
	DirectIP          *approvalstate.DirectIPCapabilityRuntime
	Gate              hitl.ApprovalGate
	HostResources     *hostresources.Service
	Listen            *approvalstate.SandboxPortGrantRuntime
	Loopback          *approvalstate.SandboxPortGrantRuntime
	Sockets           *approvalstate.SocketCapabilityRuntime
	Store             session.Store
	WriteRoots        *approvalstate.SandboxPathGrantRuntime
	responses         *httpio.Responder
}

type HeldValues struct {
	Access    *Access
	Vault     *presence.Unlocks
	responses *httpio.Responder
}

func New(responses *httpio.Responder, deps Deps) Handler {
	checkpoints, _ := deps.Checkpoints.(*hitl.Checkpoints)
	var options hitl.ApprovalOptionResolver
	var presence *hitl.VaultPresence
	if checkpoints != nil {
		options = checkpoints.Authority
		presence = checkpoints.Presence
	}
	httpio.RequireDependencies("capabilityadmin",
		httpio.Required{Name: "responses", Present: responses != nil},
		httpio.Required{Name: "Checkpoints", Present: options != nil},
		httpio.Required{Name: "Projects", Present: deps.Projects != nil},
		httpio.Required{Name: "Store", Present: deps.Store != nil},
		httpio.Required{Name: "ApprovalDecisions", Present: deps.ApprovalDecisions != nil},
		httpio.Required{Name: "Gate", Present: deps.Gate != nil},
		httpio.Required{Name: "Settings.Approvals", Present: deps.Settings != nil && deps.Settings.Approvals != nil},
	)
	authorityMu := &sync.Mutex{}
	h := Handler{}
	h.Access = &Access{Gate: deps.Gate, Events: deps.Events, Projects: deps.Projects, Settings: deps.Settings, Store: deps.Store, responses: responses}
	h.CheckpointActions = &CheckpointActions{Checkpoints: deps.Checkpoints, Events: deps.Events, ManagedSecrets: deps.ManagedSecrets, Projects: deps.Projects, Store: deps.Store, options: options, presence: presence, responses: responses}
	h.Grants = &Grants{Gate: deps.Gate, AuthzRecorder: deps.Authority.AuthzRecorder, ChatGrants: deps.Authority.ChatGrants, Events: deps.Events, GrantedPaths: deps.Authority.GrantedPaths, Listen: deps.Authority.Listen, Loopback: deps.Authority.Loopback, Projects: deps.Projects, ReadPaths: deps.Authority.ReadPaths, Settings: deps.Settings, Sockets: deps.Authority.Sockets, WriteRoots: deps.Authority.WriteRoots, authorityMu: authorityMu, responses: responses}
	h.Installation = &Installation{Gate: deps.Gate, DirectIP: deps.Authority.DirectIP, GrantedPaths: deps.Authority.GrantedPaths, LLMService: deps.LLMService, Listen: deps.Authority.Listen, Loopback: deps.Authority.Loopback, ReadPaths: deps.Authority.ReadPaths, Sockets: deps.Authority.Sockets, WriteRoots: deps.Authority.WriteRoots, authorityMu: authorityMu}
	h.Inventory = &Inventory{Gate: deps.Gate, ApprovalDecisions: deps.Authority.ApprovalDecisions, AuthzRecorder: deps.Authority.AuthzRecorder, DirectIP: deps.Authority.DirectIP, HostResources: deps.HostResources, Listen: deps.Authority.Listen, Loopback: deps.Authority.Loopback, Sockets: deps.Authority.Sockets, Store: deps.Store, WriteRoots: deps.Authority.WriteRoots, responses: responses}
	h.HeldValues = &HeldValues{Vault: deps.Authority.Vault, responses: responses}
	h.Access.Grants = h.Grants
	h.Access.Inventory = h.Inventory
	h.Grants.Inventory = h.Inventory
	h.HeldValues.Access = h.Access
	return h
}
