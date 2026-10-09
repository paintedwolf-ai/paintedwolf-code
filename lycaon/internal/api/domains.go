package api

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/api/gitadmin"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/scanadmin"
	"github.com/lycaon/lycaon/internal/attention"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/browser/preview"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/harnessfixture"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostidentity"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/preflight"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectliveness"
	"github.com/lycaon/lycaon/internal/promptattach"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/webindex"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
)

type Activity struct {
	attachmentCaps      promptattach.Caps
	ShuttingDown        func() <-chan struct{}
	editorClients       *editordoc.ClientLiveness
	agentPresence       *agentpresence.Tracker
	attention           *attention.Source
	costTracker         cost.CostTracker
	dataDir             string
	events              events.ReplayHub
	healthPayload       func() healthResponse
	hostIdentity        hostidentity.Identity
	hostResources       *hostresources.Service
	preflightEnv        preflight.Env
	projectLiveness     *projectliveness.Tracker
	projectRegistry     project.Registry
	responses           *httpio.Responder
	scheduleSourceWatch func(context.Context, string)
	sessionStore        session.Store
	settingsSvc         *settings.Service
}

type Artifacts struct {
	projectRegistry project.Registry
	responses       *httpio.Responder
	sessionStore    session.Store
	visualStore     visual.Store
}

type Conversation struct {
	ShuttingDown  func() <-chan struct{}
	preview       *preview.Controller
	progressStore progress.Store
	responses     *httpio.Responder
	sessionStore  session.Store
	sessions      *session.Manager
}

type Findings struct {
	Scan            *scanadmin.Handler
	projectRegistry project.Registry
	responses       *httpio.Responder
	scanCadence     *scancadence.Service
}

type HarnessControl struct {
	HarnessPreparation *HarnessPreparation
	HarnessProviders   *HarnessProviders
	Workflow           *workflow.RunManager
	checkpoints        hitl.CheckpointManager
	eventPublisher     *events.Publisher
	harness            *harnessServices
	manualLLM          *llm.ManualProvider
	requireClientAuth  func(http.Handler) http.Handler
	responses          *httpio.Responder
	router             chi.Router
	sessionStore       session.Store
	sessions           *session.Manager
	visualStore        visual.Store
}

type HarnessPreparation struct {
	Workflow        *workflow.RunManager
	dataDir         string
	database        db.Handle
	harness         *harnessServices
	harnessWorkers  *harnessfixture.Workers
	llmSvc          *llm.Service
	projectRegistry project.Registry
	responses       *httpio.Responder
	sessionStore    session.Store
	sessions        *session.Manager
	workers         worker.WorkerQueue
}

type HarnessProviders struct {
	costTracker cost.CostTracker
	llmSvc      *llm.Service
	responses   *httpio.Responder
}

type LocalData struct {
	extensionOwner   *extensionstate.Owner
	dataDir          string
	localData        *localdata.Registry
	responses        *httpio.Responder
	sessions         *session.Manager
	sourceLedger     *sourceledger.Store
	webIndex         *webindex.Store
	workerBranchRoot string
	workerSeedRoot   string
	workers          worker.WorkerQueue
}

type Storage struct {
	dataDir            string
	database           db.Handle
	markRestorePending func()
	recovery           *recoveryState
	responses          *httpio.Responder
	storePath          string
}

type Workers struct {
	Git             *gitadmin.Handler
	board           *board.SnapshotBuilder
	delegations     *delegation.Manager
	projectRegistry project.Registry
	responses       *httpio.Responder
	sessionStore    session.Store
	workerCancel    *worker.CancelService
	workers         worker.WorkerQueue
}

func composeDomains(s *Server, deps Dependencies) {
	s.Routes.Activity = &Activity{attachmentCaps: s.Admin.Prompt.Submission.Caps, ShuttingDown: s.ShuttingDown, editorClients: s.Sources.Editor.EditorClients, agentPresence: deps.Source.AgentPresence, attention: deps.Host.Attention, costTracker: deps.Providers.CostTracker, dataDir: s.dataDir, events: s.events, healthPayload: s.healthPayload, hostIdentity: deps.Host.HostIdentity, hostResources: deps.Host.HostResources, preflightEnv: deps.Host.PreflightEnv, projectLiveness: deps.Source.ProjectLiveness, projectRegistry: deps.Core.Projects, responses: &s.responses, scheduleSourceWatch: s.Sources.Watch.ScheduleSourceWatch, sessionStore: s.sessionStore, settingsSvc: deps.Core.Settings}
	s.Routes.Artifacts = &Artifacts{projectRegistry: deps.Core.Projects, responses: &s.responses, sessionStore: s.sessionStore, visualStore: deps.Source.VisualStore}
	s.Routes.Conversation = &Conversation{ShuttingDown: s.ShuttingDown, preview: deps.Host.Preview, progressStore: deps.Source.ProgressStore, responses: &s.responses, sessionStore: s.sessionStore, sessions: s.sessions}
	s.Routes.Findings = &Findings{Scan: &s.Admin.Scan, projectRegistry: deps.Core.Projects, responses: &s.responses, scanCadence: deps.Scans.ScanCadence}
	s.Routes.HarnessControl = &HarnessControl{Workflow: deps.Workflow.Workflows, checkpoints: deps.Approvals.Checkpoints, eventPublisher: s.eventPublisher, harness: &s.harness, manualLLM: deps.Harness.ManualLLM, requireClientAuth: s.requireClientAuth, responses: &s.responses, router: s.router, sessionStore: s.sessionStore, sessions: s.sessions, visualStore: deps.Source.VisualStore}
	s.Routes.HarnessPreparation = &HarnessPreparation{Workflow: deps.Workflow.Workflows, dataDir: s.dataDir, database: deps.Core.Database, harness: &s.harness, harnessWorkers: deps.Harness.HarnessWorkers, llmSvc: deps.Providers.LLM, projectRegistry: deps.Core.Projects, responses: &s.responses, sessionStore: s.sessionStore, sessions: s.sessions, workers: deps.Workflow.Workers}
	s.Routes.HarnessProviders = &HarnessProviders{costTracker: deps.Providers.CostTracker, llmSvc: deps.Providers.LLM, responses: &s.responses}
	s.Routes.LocalData = &LocalData{extensionOwner: s.Admin.Extensions.Mutations.Owner, dataDir: s.dataDir, responses: &s.responses, sessions: s.sessions, sourceLedger: s.Sources.Workspace.SourceLedger, webIndex: deps.External.WebIndex, workerBranchRoot: s.workerBranchRoot, workerSeedRoot: s.workerSeedRoot, workers: deps.Workflow.Workers}
	s.Routes.Storage = &Storage{dataDir: s.dataDir, database: deps.Core.Database, markRestorePending: s.markRestorePending, recovery: s.recovery, responses: &s.responses, storePath: s.storePath}
	s.Routes.Workers = &Workers{Git: &s.Admin.Git, board: deps.Workflow.Board, delegations: deps.Workflow.Delegations, projectRegistry: deps.Core.Projects, responses: &s.responses, sessionStore: s.sessionStore, workerCancel: deps.Workflow.WorkerCancel, workers: deps.Workflow.Workers}
	s.Routes.HarnessControl.HarnessPreparation = s.Routes.HarnessPreparation
	s.Routes.HarnessControl.HarnessProviders = s.Routes.HarnessProviders
}
