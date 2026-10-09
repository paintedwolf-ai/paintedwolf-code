package api

import (
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/api/capabilityadmin"
	"github.com/lycaon/lycaon/internal/api/extensionadmin"
	"github.com/lycaon/lycaon/internal/api/gitadmin"
	"github.com/lycaon/lycaon/internal/api/historyadmin"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/mcpadmin"
	"github.com/lycaon/lycaon/internal/api/modeladmin"
	"github.com/lycaon/lycaon/internal/api/projectadmin"
	"github.com/lycaon/lycaon/internal/api/promptadmin"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/api/researchadmin"
	"github.com/lycaon/lycaon/internal/api/scanadmin"
	"github.com/lycaon/lycaon/internal/api/sessionadmin"
	"github.com/lycaon/lycaon/internal/api/sessionview"
	"github.com/lycaon/lycaon/internal/api/settingsadmin"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/api/workflowadmin"
	"github.com/lycaon/lycaon/internal/attention"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/browser/preview"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/harnessfixture"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostidentity"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/people/personactions"
	"github.com/lycaon/lycaon/internal/preflight"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectliveness"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/version"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/webindex"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type Server struct {
	Extensions      extensionadmin.Handler
	Scan            scanadmin.Handler
	Settings        settingsadmin.Handler
	Capabilities    capabilityadmin.Handler
	Prompt          promptadmin.Handler
	Project         projectadmin.Handler
	Workflow        workflowadmin.Handler
	SessionAdmin    sessionadmin.Handler
	SessionView     sessionview.Projector
	Git             gitadmin.Handler
	Sources         sourceapi.Handler
	mcpAdmin        *mcpadmin.Handler
	researchAdmin   *researchadmin.Handler
	modelAdmin      *modeladmin.Handler
	historyAdmin    *historyadmin.Handler
	router          chi.Router
	background      taskgroup.Group
	responses       httpio.Responder
	apiToken        string
	rateLimits      *rateLimitState
	recovery        *recoveryState
	restorePending  atomic.Bool
	localData       *localdata.Registry
	searchPages     *searchPageCache
	sessionStore    session.Store
	rerank          decide.Reranker
	personActions   *personactions.Store
	sessions        *session.Manager
	projectRegistry project.Registry
	llmSvc          *llm.Service
	settingsSvc     *settings.Service
	events          events.ReplayHub
	eventPublisher  *events.Publisher
	presence        *events.Presence
	hostIdentity    hostidentity.Identity
	delegations     *delegation.Manager
	workers         worker.WorkerQueue
	workerCancel    *worker.CancelService
	board           *board.SnapshotBuilder
	scanCadence     *scancadence.Service
	costTracker     cost.CostTracker
	checkpoints     hitl.CheckpointManager
	webIndex        *webindex.Store
	progressStore   progress.Store
	visualStore     visual.Store
	manualLLM       *llm.ManualProvider
	harnessWorkers  *harnessfixture.Workers
	preflightEnv    preflight.Env
	attention       *attention.Source
	agentPresence   *agentpresence.Tracker
	projectLiveness *projectliveness.Tracker
	preview         *preview.Controller
	hostResources   *hostresources.Service
	database        db.Handle
	harness         harnessServices
	storagePaths
	health
}

// Route families cross-reference each other through fixed addresses in s, so construction order is safe.
func NewServer(deps Dependencies, logger *slog.Logger, token string) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	requireDependencies(deps)
	s := &Server{
		responses:       httpio.Responder{Logger: logger, Notices: deps.UserNotices},
		apiToken:        strings.TrimSpace(token),
		searchPages:     newSearchPageCache(),
		sessionStore:    deps.Store,
		rerank:          deps.Rerank,
		personActions:   deps.PersonActions,
		sessions:        deps.Sessions,
		projectRegistry: deps.Projects,
		llmSvc:          deps.LLM,
		settingsSvc:     deps.Settings,
		events:          deps.Events,
		eventPublisher:  deps.EventPublisher,
		presence:        deps.Presence,
		hostIdentity:    deps.HostIdentity,
		delegations:     deps.Delegations,
		workers:         deps.Workers,
		workerCancel:    deps.WorkerCancel,
		board:           deps.Board,
		scanCadence:     deps.ScanCadence,
		costTracker:     deps.CostTracker,
		checkpoints:     deps.Checkpoints,
		webIndex:        deps.WebIndex,
		progressStore:   deps.ProgressStore,
		visualStore:     deps.VisualStore,
		manualLLM:       deps.ManualLLM,
		harnessWorkers:  deps.HarnessWorkers,
		preflightEnv:    deps.PreflightEnv,
		attention:       deps.Attention,
		agentPresence:   deps.AgentPresence,
		projectLiveness: deps.ProjectLiveness,
		preview:         deps.Preview,
		hostResources:   deps.HostResources,
		database:        deps.Database,
		storagePaths: storagePaths{
			dataDir:          strings.TrimSpace(deps.DataDir),
			storePath:        strings.TrimSpace(deps.StorePath),
			workerBranchRoot: strings.TrimSpace(deps.WorkerBranchRoot),
			workerSeedRoot:   strings.TrimSpace(deps.WorkerSeedRoot),
		},
		health: health{
			storeRevision:      deps.StoreRevision,
			previousAppVersion: strings.TrimSpace(deps.PreviousAppVersion),
			minDenVersion:      strings.TrimSpace(deps.MinDenVersion),
		},
	}
	s.SessionView = sessionview.New(sessionview.Projector{Workflows: deps.Workflows, Store: deps.Store, Sessions: deps.Sessions, Projects: deps.Projects})
	s.Git = gitadmin.New(&s.responses, gitadmin.Deps{
		Board: deps.Board, CommitDrafter: deps.CommitDrafter, LLMService: deps.LLM, ProjectRegistry: deps.Projects,
		RepoSetCache: deps.RepoSetCache, SessionStore: deps.Store, Sessions: deps.Sessions, DataDir: s.dataDir,
	})
	s.Sources = sourceapi.New(&s.responses, &s.background, sourceOperations, sourceapi.Deps{
		Git: &s.Git, MutationGate: deps.MutationGate,
		CatalogSnapshot: deps.CatalogSnapshot, WatchNeedsSeed: deps.WatchNeedsSeed,
		EditorClients: deps.EditorClients, EditorDocuments: deps.EditorDocuments, Events: deps.Events,
		FileBriefings: deps.FileBriefings, FileOperations: deps.FileOperations, ManagedSecrets: deps.ManagedSecrets,
		ProjectRegistry: deps.Projects, ScanCadence: deps.ScanCadence,
		SecretSpans: deps.SecretSpans, SessionStore: deps.Store, SourceInventory: deps.SourceInventory,
		SourceLedger: deps.SourceLedger, SourceMutations: deps.SourceMutations, VisualStore: deps.VisualStore,
		Workers: deps.Workers, AttachmentStore: s.Prompt.AttachmentStore, TryRunPromotion: s.Project.TryRunPromotion,
	})
	s.Prompt = promptadmin.New(&s.responses, &s.background, promptadmin.Deps{
		DataDir: s.dataDir, EventPublisher: deps.EventPublisher, HintConfig: deps.HintConfig,
		ManagedSecrets: deps.ManagedSecrets, Projects: deps.Projects, Store: deps.Store, Sessions: deps.Sessions,
		VisualStore: deps.VisualStore, Sources: &s.Sources, Video: deps.Video,
	})
	s.SessionAdmin = sessionadmin.New(&s.responses, &s.background, sessionadmin.Deps{
		Checkpoints: deps.Checkpoints, EventPublisher: deps.EventPublisher, Events: deps.Events,
		FileAgeWarmer: deps.FileAgeWarmer, Invocations: deps.Invocations, LLMService: deps.LLM, Preview: deps.Preview,
		ProgressStore: deps.ProgressStore, Projects: deps.Projects, ProjectRules: deps.ProjectRules, Store: deps.Store,
		Sessions: deps.Sessions, Workers: deps.Workers, Workflows: deps.Workflows, Settings: deps.Settings,
		Sources: &s.Sources, SessionView: &s.SessionView, Git: &s.Git, Prompt: &s.Prompt,
	})
	s.Workflow = workflowadmin.New(&s.responses, &s.background, workflowadmin.Deps{
		Workflows: deps.Workflows, Catalog: deps.WorkflowCatalog, Runs: deps.WorkflowRuns,
		Composer: deps.WorkflowComposer, Persister: deps.WorkflowPersister, Blueprints: deps.Blueprints,
		Orchestrator: deps.Orchestrator, EventPublisher: deps.EventPublisher, ManagedSecrets: deps.ManagedSecrets,
		Projects: deps.Projects, Scans: deps.ScanCoordinator, Store: deps.Store, Sessions: deps.Sessions,
		VisualStore: deps.VisualStore, Workers: deps.Workers, SessionAdmin: &s.SessionAdmin, SessionView: &s.SessionView,
	})
	s.Scan = scanadmin.New(&s.responses, scanadmin.Deps{
		DetectionPacksDir: s.dataDir, PublishDetections: deps.PublishDetections, GateRepeatLedger: deps.GateRepeatLedger,
		Registry: deps.ScannerRegistry, ModuleRoot: deps.ModuleRoot, Projects: deps.Projects, Cadence: deps.ScanCadence,
		Coordinator: deps.ScanCoordinator, Sessions: deps.Sessions, Settings: deps.Settings,
	})
	publisher, emitter := extensionadmin.OwnerSeams(&s.Extensions)
	s.Extensions = extensionadmin.New(&s.responses, &s.background, extensionadmin.Deps{
		Owner: &extensionstate.Owner{Views: deps.ExtensionViews, Scanners: deps.ExtensionScanners,
			Publisher: publisher, Events: emitter, Journal: deps.ExtensionJournal}, Runtime: deps.Contributions, Events: deps.Events, MCPRegistry: deps.MCP,
		ModuleRoot: deps.ModuleRoot, Projects: deps.Projects, Store: deps.Store, Sessions: deps.Sessions,
		Settings: deps.Settings, Workflow: &s.Workflow, Prompt: &s.Prompt, Scan: &s.Scan,
	})
	s.Project = projectadmin.New(&s.responses, &s.background, projectadmin.Deps{
		Extensions: &s.Extensions, Board: deps.Board, Database: deps.Database, DataDir: s.dataDir, Events: deps.Events, LLMService: deps.LLM,
		SecretIgnores: deps.SecretIgnores, ManagedSecrets: deps.ManagedSecrets, MutationGate: deps.MutationGate, Registry: deps.Projects,
		ProjectRules: deps.ProjectRules, ScanCadence: deps.ScanCadence, Store: deps.Store, Sessions: deps.Sessions,
		Settings: deps.Settings, WorkerSeedRoot: s.workerSeedRoot, WorkerBranchRoot: s.workerBranchRoot,
		Workers: deps.Workers, Sources: &s.Sources, Git: &s.Git,
	})
	s.Project.InitProjectRemoval()
	s.Capabilities = capabilityadmin.New(&s.responses, capabilityadmin.Deps{
		HostResources: deps.HostResources,
		Authority:     deps.Authority, Gate: deps.ApprovalGate, Checkpoints: deps.Checkpoints, Events: deps.Events,
		LLMService: deps.LLM, ManagedSecrets: deps.ManagedSecrets, Projects: deps.Projects, Store: deps.Store,
		Settings: deps.Settings,
	})
	s.Settings = settingsadmin.New(&s.responses, settingsadmin.Deps{
		Pricing: deps.Pricing, Power: deps.HostPower, Events: deps.Events, Projects: deps.Projects,
		Sessions: deps.Sessions, Service: deps.Settings, Sources: &s.Sources,
	})
	s.modelAdmin = modeladmin.New(deps.LLM, deps.Projects, deps.Events, &s.responses)
	s.historyAdmin = historyadmin.New(deps.HistoryStorage, &s.responses)
	s.mcpAdmin = mcpadmin.New(deps.MCP, deps.Projects, &s.responses)
	s.researchAdmin = researchadmin.New(&s.responses, researchadmin.Deps{
		Runtime: deps.WebResearch, Discoverer: deps.WebDiscoverer, Index: deps.WebIndex, Events: deps.Events,
	})
	s.observeDependencies(deps)
	s.router = chi.NewRouter()
	s.setupMiddleware()
	s.setupRoutes()
	return s
}

func (s *Server) setupMiddleware() {
	s.router.Use(middleware.RequestID)
	s.router.Use(observability.PerformanceHTTPMiddleware())
	s.router.Use(observability.HTTPDebugMiddleware(observability.HTTPBodyCapturePolicy{
		CredentialRequest: requestBodyCarriesCredential,
		WithholdResponse:  responseBodyMustBeWithheld,
	}))
	// Suppressed in tests; targets interactive terminals.
	if os.Getenv("LYCAON_TEST") != "1" {
		s.router.Use(requestLogger())
	}
	s.router.Use(cors.Handler(corsOptions()))
	s.router.Use(s.recoverHTTPPanics)
}

// rejectEmptyPathSegments rejects ambiguous route parameters.
func (s *Server) rejectEmptyPathSegments(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "//") {
			s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "url path contains empty segment")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// markUserPresence treats mutating requests as user activity.
func (s *Server) markUserPresence(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			s.presence.MarkUserAction()
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) setupRoutes() {
	s.rateLimits = newRateLimitState()
	registerRootOperation(s.router, operationGetHealth, s.handleHealth)
	s.router.Route("/v1", func(r chi.Router) {
		r.Use(s.requireClientAuth)
		r.Use(s.bindCaller)
		r.Use(s.rejectRestorePending)
		r.Use(s.rejectEmptyPathSegments)
		r.Use(s.markUserPresence)
		s.registerV1Operation(r, operationGetHost, s.handleGetHost)
		s.registerV1Operation(r, operationGetPreflight, s.handlePreflight)
		s.registerV1Operation(r, operationExportDiagnostics, s.handleExportDiagnostics)
		s.registerV1Operation(r, operationCreateSession, s.SessionAdmin.HandleCreateSession)
		s.registerV1Operation(r, operationGetSession, s.handleGetSession)
		s.registerV1Operation(r, operationGetSessionBootstrap, s.SessionAdmin.HandleSessionBootstrap)
		s.registerV1Operation(r, operationUpdateSession, s.SessionAdmin.HandleUpdateSession)
		s.registerV1Operation(r, operationDeleteSession, s.SessionAdmin.HandleDeleteSession)
		s.registerV1Operation(r, operationListSessionMessages, s.SessionAdmin.HandleListSessionMessages)
		s.registerV1Operation(r, operationGetChatContent, s.Prompt.HandleGetChatContent)
		s.registerV1Operation(r, operationSearchChatContent, s.Prompt.HandleSearchChatContent)
		s.registerV1Operation(r, operationResolveMessageNavigation, s.SessionAdmin.HandleResolveMessageNavigation)
		s.registerV1Operation(r, operationListSessionInvocations, s.SessionAdmin.HandleListSessionInvocations)
		s.registerV1Operation(r, operationExportSessionTranscript, s.SessionAdmin.HandleExportSessionTranscript)
		s.registerV1Operation(r, operationSendPrompt, s.SessionAdmin.WithSessionPanicRecovery(s.Prompt.HandlePrompt))
		s.registerV1Operation(r, operationCreateComposerSecret, s.Prompt.HandleCreateComposerSecret)
		s.registerV1Operation(r, operationUploadAttachment, s.Prompt.HandleUploadAttachment)
		s.registerContributionRoutes(r)
		s.registerV1Operation(r, operationAbortSession, s.SessionAdmin.WithSessionPanicRecovery(s.SessionAdmin.HandleAbortSession))
		s.registerV1Operation(r, operationPreviewSessionRewind, s.SessionAdmin.WithSessionPanicRecovery(s.SessionAdmin.HandlePreviewSessionRewind))
		s.registerV1Operation(r, operationRewindSession, s.SessionAdmin.WithSessionPanicRecovery(s.SessionAdmin.HandleRewindSession))
		s.registerV1Operation(r, operationMarkSessionSeen, s.SessionAdmin.HandleMarkSessionSeen)
		s.registerV1Operation(r, operationStreamSession, s.handleStream)
		s.registerV1Operation(r, operationCompactSession, s.Prompt.HandleSessionCompact)
		s.registerV1Operation(r, operationGetSessionContext, s.Prompt.HandleSessionContext)
		s.registerV1Operation(r, operationGetSessionFindings, s.handleSessionFindings)
		s.registerV1Operation(r, operationListDraftVersions, s.Prompt.HandleListDraftVersions)
		s.registerV1Operation(r, operationGetSessionProgress, s.handleSessionProgress)
		s.registerV1Operation(r, operationListSessionArtifacts, s.handleListSessionArtifacts)
		s.registerV1Operation(r, operationCreateSessionArtifact, s.handleCreateSessionArtifact)
		s.registerV1Operation(r, operationGetSessionArtifact, s.handleSessionArtifact)
		s.registerV1Operation(r, operationListSessionBackgroundProcesses, s.handleListBackgroundProcesses)
		s.registerV1Operation(r, operationGetSessionBackgroundProcessOutput, s.handleGetBackgroundProcessOutput)
		s.registerV1Operation(r, operationStopBackgroundProcess, s.handleStopBackgroundProcess)
		s.registerV1Operation(r, operationListSessionPreviews, s.handleListSessionPreviews)
		s.registerV1Operation(r, operationWatchPreview, s.handleWatchPreview)
		s.registerV1Operation(r, operationGetSessionQueue, s.Prompt.HandleGetQueue)
		s.registerV1Operation(r, operationUpdateSessionQueue, s.Prompt.HandleUpdateSessionQueue)
		s.registerV1Operation(r, operationGetCoordinatorContext, s.Prompt.HandleCoordinatorContext)
		s.registerV1Operation(r, operationListCheckpoints, s.Capabilities.HandleListCheckpoints)
		s.registerV1Operation(r, operationResolveCheckpoint, s.Capabilities.HandleResolveCheckpoint)
		s.registerV1Operation(r, operationBeginCheckpointUnlockChallenge, s.Capabilities.HandleBeginUnlockChallenge)
		s.registerV1Operation(r, operationGetChatVault, s.Capabilities.HandleGetChatVault)
		s.registerV1Operation(r, operationLockChatVault, s.Capabilities.HandleLockChatVault)
		s.registerV1Operation(r, operationLockVault, s.Capabilities.HandleLockVault)
		s.registerV1Operation(r, operationListWorkflows, s.Workflow.HandleListWorkflows)
		s.registerV1Operation(r, operationListWorkflowTemplates, s.Workflow.HandleListWorkflowTemplates)
		s.registerV1Operation(r, operationComposeWorkflow, s.Workflow.HandleComposeWorkflow)
		s.registerV1Operation(r, operationComposeWorkflowFromTemplate, s.Workflow.HandleComposeFromTemplate)
		s.registerV1Operation(r, operationPersistWorkflow, s.Workflow.HandlePersistWorkflow)
		s.registerV1Operation(r, operationStartWorkflowRun, s.Workflow.HandleStartWorkflowRun)
		s.registerV1Operation(r, operationExitWorkflowRun, s.Workflow.HandleExitWorkflowRun)
		s.registerV1Operation(r, operationListSessionWorkflowRuns, s.Workflow.HandleListSessionWorkflowRuns)
		s.registerV1Operation(r, operationGetActiveWorkflowRun, s.Workflow.HandleGetActiveWorkflowRun)
		s.registerV1Operation(r, operationGetWorkflowRun, s.Workflow.HandleGetWorkflowRun)
		s.registerV1Operation(r, operationGetWorkflowRunReport, s.Workflow.HandleGetWorkflowRunReport)
		s.registerV1Operation(r, operationPauseWorkflowRun, s.Workflow.HandlePauseWorkflowRun)
		s.registerV1Operation(r, operationResumeWorkflowRun, s.Workflow.HandleResumeWorkflowRun)
		s.registerV1Operation(r, operationCancelWorkflowRun, s.Workflow.HandleCancelWorkflowRun)
		s.registerV1Operation(r, operationAdvanceWorkflowRun, s.Workflow.HandleAdvanceWorkflowRun)
		s.registerV1Operation(r, operationFireWorkflowTransition, s.Workflow.HandleFireWorkflowTransition)
		s.registerV1Operation(r, operationResolveWorkflowDecision, s.Workflow.HandleResolveWorkflowDecision)
		s.registerV1Operation(r, operationResolveWorkflowFeedback, s.Workflow.HandleResolveWorkflowFeedback)
		s.registerV1Operation(r, operationResolveWorkflowSecret, s.Workflow.HandleResolveWorkflowSecret)

		s.registerV1Operation(r, operationListBlueprints, s.Workflow.HandleListBlueprints)
		s.registerV1Operation(r, operationCreateBlueprint, s.Workflow.HandleCreateBlueprint)
		s.registerV1Operation(r, operationGetBlueprint, s.Workflow.HandleGetBlueprint)
		s.registerV1Operation(r, operationUpdateBlueprint, s.Workflow.HandleUpdateBlueprint)
		s.registerV1Operation(r, operationDeleteBlueprint, s.Workflow.HandleDeleteBlueprint)
		s.registerV1Operation(r, operationApproveBlueprint, s.Workflow.HandleApproveBlueprint)
		s.registerV1Operation(r, operationLaunchBlueprint, s.Workflow.HandleLaunchBlueprint)

		s.registerProjectRoutes(r)

		s.registerV1Operation(r, operationListProviders, s.modelAdmin.ListProviders)
		s.registerV1Operation(r, operationListProviderKinds, s.modelAdmin.ListProviderKinds)
		s.registerV1Operation(r, operationCreateProvider, s.modelAdmin.CreateProvider)
		s.registerV1Operation(r, operationUpdateProvider, s.modelAdmin.UpdateProvider)
		s.registerV1Operation(r, operationDeleteProvider, s.modelAdmin.DeleteProvider)
		s.registerV1Operation(r, operationReplaceProviderCredential, s.modelAdmin.SetCredential)
		s.registerV1Operation(r, operationDeleteProviderCredential, s.modelAdmin.DeleteCredential)
		s.registerV1Operation(r, operationTestProvider, s.modelAdmin.TestProvider)
		s.registerV1Operation(r, operationRefreshProviderModels, s.modelAdmin.RefreshModels)
		s.registerV1Operation(r, operationGetModelPolicySettings, s.modelAdmin.GetPolicy)
		s.registerV1Operation(r, operationUpdateModelPolicySettings, s.modelAdmin.UpdatePolicy)

		s.registerSettingsRoutes(r)

		s.registerV1Operation(r, operationGetCostSummary, s.handleCostSummary)
		s.registerV1Operation(r, operationGetProjectCostReport, s.handleProjectCostReport)

		s.registerV1Operation(r, operationCreateDelegation, s.handleCreateDelegation)
		s.registerV1Operation(r, operationGetDelegation, s.handleGetDelegation)
		s.registerV1Operation(r, operationListDelegationLegs, s.handleListDelegationLegs)
		s.registerV1Operation(r, operationDispatchDelegationLeg, s.handleDispatchDelegationLeg)
		s.registerV1Operation(r, operationAbortDelegation, s.handleAbortDelegation)

		s.registerV1Operation(r, operationListWorkers, s.handleListWorkers)
		s.registerV1Operation(r, operationCancelWorker, s.handleWorkerCancel)
		s.registerV1Operation(r, operationGetWorkerChanges, s.Sources.HandleGetWorkerChanges)

		s.registerV1Operation(r, operationGetBoard, s.handleGetBoard)
		s.registerV1Operation(r, operationSearch, s.handleSearch)
		s.registerV1Operation(r, operationPreviewSearchReplacement, s.handlePreviewSearchReplacement)
		s.registerV1Operation(r, operationApplySearchReplacement, s.handleApplySearchReplacement)
		s.registerV1Operation(r, operationExportSearchResults, s.handleExportSearchResults)
		s.registerV1Operation(r, operationSubscribeEvents, s.handleEvents)
		s.registerV1Operation(r, operationGetAttention, s.handleGetAttention)

		s.registerScanRoutes(r)
		s.registerMCPRoutes(r)
		s.registerWebResearchRoutes(r)
		s.registerHostResourceRoutes(r)

		s.registerV1Operation(r, operationGetLocalData, s.handleGetLocalData)
		s.registerV1Operation(r, operationGetHistoryStorage, s.historyAdmin.Status)
		s.registerV1Operation(r, operationUpdateHistoryStorage, s.historyAdmin.UpdateStorage)
		s.registerV1Operation(r, operationPreviewHistoryRetention, s.historyAdmin.Preview)
		s.registerV1Operation(r, operationPruneHistory, s.historyAdmin.Prune)
		s.registerV1Operation(r, operationCreateHistoryProtection, s.historyAdmin.CreateProtection)
		s.registerV1Operation(r, operationDeleteHistoryProtection, s.historyAdmin.DeleteProtection)
		s.registerV1Operation(r, operationClearLocalData, s.handleClearLocalData)

		s.registerV1Operation(r, operationExportBackup, s.handleBackup)
		s.registerV1Operation(r, operationGetBackupCapabilities, s.handleBackupCapabilities)
		s.registerV1Operation(r, operationRestoreBackup, s.handleRestoreBackup)
		s.registerV1Operation(r, operationRestoreRecoverySnapshot, s.handleRestoreRecoverySnapshot)
		s.registerV1Operation(r, operationResetStore, s.handleResetStore)

		s.registerExtensionsRoutes(r)
		s.registerGitRoutes(r)

		s.registerV1Operation(r, operationRecordDenPerfEvents, s.handleDenPerfEvents)
	})
	if configdir.IsHarnessChannel() {
		s.registerHarnessRoutes()
	}
	s.router.NotFound(s.handleRouteNotFound)
	s.router.MethodNotAllowed(s.handleMethodNotAllowed)
}

// sourceOperations are the routes whose file requests the source handler journals and replays.
var sourceOperations = sourceapi.Operations{
	CopyProjectSource:        toSourceOperation(operationCopyProjectSource),
	CreateProjectSourceEntry: toSourceOperation(operationCreateProjectSourceEntry),
	DeleteProjectSource:      toSourceOperation(operationDeleteProjectSource),
	RedoProjectSourceHistory: toSourceOperation(operationRedoProjectSourceHistory),
	RenameProjectSource:      toSourceOperation(operationRenameProjectSource),
	UndoProjectSourceHistory: toSourceOperation(operationUndoProjectSourceHistory),
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func (s *Server) rejectRestorePending(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.restorePending.Load() {
			s.responses.Fail(w, wire.ApiErrorCodeBackupRestorePending,
				"restore is staged; restart before making another request")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) markRestorePending() {
	s.restorePending.Store(true)
}

type healthResponse struct {
	Status                    string `json:"status"`
	Version                   string `json:"version"`
	StoreRevision             uint64 `json:"store_revision"`
	SchemaVersion             int    `json:"schema_version"`
	MinDenVersion             string `json:"min_den_version,omitempty"`
	PreviousAppVersion        string `json:"previous_app_version,omitempty"`
	StoreSchemaVersion        *int   `json:"store_schema_version,omitempty"`
	RecoveryReason            string `json:"recovery_reason,omitempty"`
	RecoveryDetail            string `json:"recovery_detail,omitempty"`
	RecoverySnapshotAvailable bool   `json:"recovery_snapshot_available"`
	RecoverySnapshotAt        string `json:"recovery_snapshot_at,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	httpio.WriteJSON(w, http.StatusOK, s.healthPayload())
}

func (s *Server) healthPayload() healthResponse {
	resp := healthResponse{
		Status:             "ok",
		Version:            version.Version,
		StoreRevision:      s.storeRevision,
		SchemaVersion:      db.SchemaVersion,
		MinDenVersion:      s.minDenVersion,
		PreviousAppVersion: s.previousAppVersion,
	}
	if st := s.recovery; st != nil {
		resp.Status = "recovery"
		storeVer := st.StoreSchemaVersion
		resp.StoreSchemaVersion = &storeVer
		resp.RecoveryReason = string(st.Reason)
		resp.RecoveryDetail = st.Detail
		resp.RecoverySnapshotAvailable = st.SnapshotAvailable
		resp.RecoverySnapshotAt = st.SnapshotAt
	}
	return resp
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sess, ok := requestscope.Session(s.sessionStore, &s.responses, w, r, id)
	if !ok {
		return
	}
	s.SessionView.HydrateSessionWorkspace(r.Context(), sess)
	s.SessionView.EnrichSession(r.Context(), sess)
	httpio.WriteJSON(w, http.StatusOK, sess)
}
