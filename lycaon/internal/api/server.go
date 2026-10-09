package api

import (
	"context"
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
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/browser/preview"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/fileops"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/harnessfixture"
	"github.com/lycaon/lycaon/internal/historyretention"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostidentity"
	"github.com/lycaon/lycaon/internal/hostpower"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/people/personactions"
	"github.com/lycaon/lycaon/internal/preflight"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectignore"
	"github.com/lycaon/lycaon/internal/projectliveness"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/scan"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretspan"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/usernotice"
	"github.com/lycaon/lycaon/internal/version"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/webindex"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
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

// storagePaths names host-managed durable storage.
type storagePaths struct {
	dataDir          string
	storePath        string
	workerBranchRoot string
	workerSeedRoot   string
}

// health is the boot metadata GET /health reports.
type health struct {
	storeRevision      uint64
	previousAppVersion string
	minDenVersion      string
}

// Dependencies are the host-supplied services the API routes over.
type Dependencies struct {
	SecretIgnores *projectignore.SecretService
	// Core stores and runtimes. Database is the host database the API's own
	// journals live in.
	Database       db.Handle
	Store          session.Store
	PersonActions  *personactions.Store
	Projects       project.Registry
	Sessions       *session.Manager
	LLM            *llm.Service
	CostTracker    cost.CostTracker
	Settings       *settings.Service
	Events         events.ReplayHub
	EventPublisher *events.Publisher
	// Rerank blends the decision engine into project search hits.
	Rerank       decide.Reranker
	Presence     *events.Presence
	UserNotices  *usernotice.Catalog
	HostIdentity hostidentity.Identity
	Invocations  invocation.Recorder
	MutationGate *project.MutationGate

	// Host storage, module root, and health metadata.
	DataDir            string
	StorePath          string
	WorkerBranchRoot   string
	WorkerSeedRoot     string
	ModuleRoot         string
	StoreRevision      uint64
	PreviousAppVersion string
	MinDenVersion      string

	// Secrets.
	ManagedSecrets *secretcap.Service
	SecretSpans    *secretspan.Screener

	// Approvals and chat-scoped authority.
	Checkpoints  hitl.CheckpointManager
	ApprovalGate hitl.ApprovalGate
	Authority    capabilityadmin.Authority

	// Scanning and detection packs (stored under DataDir).
	ScanCoordinator   scan.ScanCoordinator
	ScannerRegistry   scan.CodeScannerRegistry
	ScanCadence       *scancadence.Service
	GateRepeatLedger  *approvalstate.GateRepeatLedger
	PublishDetections func(*detectionpack.Matcher)

	// Workflows, delegation, and workers.
	Workflows         *workflow.RunManager
	WorkflowCatalog   workflowcatalog.Resolver
	WorkflowRuns      *runstate.Repository
	WorkflowComposer  *workflowcomposition.Composer
	WorkflowPersister *workflowcomposition.Persister
	Blueprints        *blueprint.Manager
	Orchestrator      orchestration.Orchestrator
	Delegations       *delegation.Manager
	Workers           worker.WorkerQueue
	WorkerCancel      *worker.CancelService
	Board             *board.SnapshotBuilder
	RepoSetCache      *git.RepoSetCache

	// Sources, editing, and session content.
	SourceLedger    *sourceledger.Store
	SourceMutations *project.SourceMutationService
	FileOperations  *fileops.Service
	FileBriefings   *filebriefing.Service
	EditorDocuments *editordoc.Service
	EditorClients   *editordoc.ClientLiveness
	AgentPresence   *agentpresence.Tracker
	ProjectLiveness *projectliveness.Tracker
	ProjectRules    *rules.ProjectRulesOverlay
	HintConfig      *guidance.HintConfig
	FileAgeWarmer   func(ctx context.Context, projectDir string)
	VisualStore     visual.Store
	ProgressStore   progress.Store

	// Extensions: NewServer builds the subsystem owner over these stores,
	// publishing through the extension routes.
	ExtensionViews    *catalogview.Cache
	ExtensionScanners extpacks.ScannerRequirementChecker
	ExtensionJournal  extensionstate.Journal
	Contributions     extensionadmin.ContributionRuntime

	// MCP, web research, and history storage.
	MCP            *mcp.RegistryImpl
	WebResearch    webresearch.Runtime
	WebDiscoverer  webresearch.DirectDiscovererFactory
	WebIndex       *webindex.Store
	HistoryStorage *historyretention.Service

	// Host services.
	HostResources *hostresources.Service
	HostPower     *hostpower.Controller
	Pricing       *settings.PricingHost
	Preview       *preview.Controller
	Video         promptattach.VideoDecoder
	PreflightEnv  preflight.Env
	Attention     *attention.Source

	// Harness channel: manual completions and scripted workers.
	ManualLLM      *llm.ManualProvider
	HarnessWorkers *harnessfixture.Workers

	// Test substitutes for host defaults.
	CommitDrafter   compaction.Summarizer
	SourceInventory sourceapi.InventoryService
	CatalogSnapshot sourceapi.CatalogSnapshotFunc
	WatchNeedsSeed  func(rootPath string) bool
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

// requireDependencies refuses to build a server without a service the host
// builds on every boot; the route families validate their own.
func requireDependencies(deps Dependencies) {
	httpio.RequireDependencies("api",
		httpio.Required{Name: "AgentPresence", Present: deps.AgentPresence != nil},
		httpio.Required{Name: "Board", Present: deps.Board != nil},
		httpio.Required{Name: "Checkpoints", Present: deps.Checkpoints != nil},
		httpio.Required{Name: "CostTracker", Present: deps.CostTracker != nil},
		httpio.Required{Name: "Database", Present: deps.Database != nil},
		httpio.Required{Name: "Delegations", Present: deps.Delegations != nil},
		httpio.Required{Name: "EventPublisher", Present: deps.EventPublisher != nil},
		httpio.Required{Name: "Events", Present: deps.Events != nil},
		httpio.Required{Name: "HostIdentity", Present: deps.HostIdentity.HostID != ""},
		httpio.Required{Name: "HostResources", Present: deps.HostResources != nil},
		httpio.Required{Name: "LLM", Present: deps.LLM != nil},
		httpio.Required{Name: "ProgressStore", Present: deps.ProgressStore != nil},
		httpio.Required{Name: "Projects", Present: deps.Projects != nil},
		httpio.Required{Name: "ScanCadence", Present: deps.ScanCadence != nil},
		httpio.Required{Name: "Sessions", Present: deps.Sessions != nil},
		httpio.Required{Name: "Settings.Approvals", Present: deps.Settings != nil && deps.Settings.Approvals != nil},
		httpio.Required{Name: "Store", Present: deps.Store != nil},
		httpio.Required{Name: "VisualStore", Present: deps.VisualStore != nil},
		httpio.Required{Name: "WorkerCancel", Present: deps.WorkerCancel != nil},
		httpio.Required{Name: "Workers", Present: deps.Workers != nil},
	)
}

// observeDependencies registers the server's reactions to its dependencies'
// changes. Each hook captures a handler at its final address in s.
func (s *Server) observeDependencies(deps Dependencies) {
	deps.SecretIgnores.Changed = s.Sources.RefreshProjectSecretScreens
	deps.ManagedSecrets.AddScreeningInvalidationObserver(s.Sources.RefreshProjectSecretScreens)
	if installer, ok := deps.Checkpoints.(hitl.ApprovalAuthorityInstallerSetter); ok {
		installer.SetApprovalAuthorityInstaller(&s.Capabilities)
	}
	deps.EditorDocuments.SetOnChange(func(ctx context.Context, change editordoc.Change) {
		s.editorDocumentChanged(ctx, change)
		d := change.Document
		_ = s.events.Publish(ctx, wire.EventTopicEditorDocument,
			events.PublishKey{Project: d.ProjectID, Facet: d.ID},
			s.Sources.EditorDocumentEventDTO(ctx, d, change.ContentChanged))
	})
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

func toSourceOperation(op generatedOperation) sourceapi.Operation {
	return sourceapi.Operation{ID: op.ID, Method: op.Method, Path: op.Path}
}

func (s *Server) registerContributionRoutes(r chi.Router) {
	s.registerV1Operation(r, operationGetContributions, s.Extensions.HandleGetContributions)
	s.registerV1Operation(r, operationInvokeProjectCommand, s.Extensions.HandleInvokeProjectCommand)
	s.registerV1Operation(r, operationInvokeSessionCommand, s.Extensions.HandleInvokeSessionCommand)
	s.registerV1Operation(r, operationResolveContributionChoices, s.Extensions.HandleContributionChoices)
	s.registerV1Operation(r, operationSearchContributionSource, s.Extensions.HandleContributionSearch)
}

func registerRootOperation(r chi.Router, operation generatedOperation, handler http.HandlerFunc) {
	r.Method(operation.Method, operation.Path, handler)
}

func (s *Server) registerV1Operation(r chi.Router, operation generatedOperation, handler http.HandlerFunc) {
	if !strings.HasPrefix(operation.Path, "/v1/") {
		panic("v1 operation path must start with /v1/")
	}
	r.Method(operation.Method, strings.TrimPrefix(operation.Path, "/v1"),
		s.authorizeOperation(operation, s.rateLimitOperation(operation, s.recordPersonAction(operation, handler)).ServeHTTP))
}

func (s *Server) registerProjectRoutes(r chi.Router) {
	s.registerV1Operation(r, operationGetSourceOperation, s.Sources.HandleGetSourceOperation)
	s.registerV1Operation(r, operationListSourceOperations, s.Sources.HandleListSourceOperations)
	s.registerV1Operation(r, operationCancelSourceOperation, s.Sources.HandleCancelSourceOperation)
	s.registerV1Operation(r, operationRetrySourceOperation, s.Sources.HandleRetrySourceOperation)
	s.registerV1Operation(r, operationListProjects, s.Project.HandleListProjects)
	s.registerV1Operation(r, operationCreateProject, s.Project.HandleCreateProject)
	s.registerV1Operation(r, operationCloneProject, s.Project.HandleCloneProject)
	s.registerV1Operation(r, operationDetectFolder, s.Extensions.HandleDetectFolder)
	s.registerV1Operation(r, operationGetProject, s.Project.HandleGetProject)
	s.registerV1Operation(r, operationListProjectSessions, s.SessionAdmin.HandleListProjectSessions)
	s.registerV1Operation(r, operationGetAgentPresence, s.handleGetAgentPresence)
	s.registerV1Operation(r, operationUpdateProject, s.Project.HandleUpdateProject)
	s.registerV1Operation(r, operationGetProjectTrust, s.Project.HandleGetProjectTrust)
	s.registerV1Operation(r, operationOpenProjectTrustReview, s.Project.HandleOpenProjectTrustReview)
	s.registerV1Operation(r, operationUpdateProjectTrust, s.Project.HandleUpdateProjectTrust)
	s.registerV1Operation(r, operationGetProjectAgentContext, s.Project.HandleGetProjectAgentContext)
	s.registerV1Operation(r, operationAssessProjectRemoval, s.Project.HandleAssessProjectRemoval)
	s.registerV1Operation(r, operationCreateProjectRemoval, s.Project.HandleCreateProjectRemoval)
	s.registerV1Operation(r, operationGetProjectRemoval, s.Project.HandleGetProjectRemoval)
	s.registerV1Operation(r, operationListProjectArtifacts, s.handleListProjectArtifacts)
	s.registerV1Operation(r, operationDeleteProjectArtifact, s.handleDeleteProjectArtifact)
	s.registerV1Operation(r, operationListProjectSecretIgnores, s.Project.HandleListSecretIgnores)
	s.registerV1Operation(r, operationCreateProjectSecretIgnore, s.Project.HandleCreateProjectSecretIgnore)
	s.registerV1Operation(r, operationDeleteProjectSecretIgnore, s.Project.HandleDeleteProjectSecretIgnore)
	s.registerV1Operation(r, operationGetSecretIgnoreCandidate, s.Project.HandleSecretIgnoreCandidate)
	s.registerV1Operation(r, operationListProjectManagedSecrets, s.Project.HandleListProjectManagedSecrets)
	s.registerV1Operation(r, operationCreateProjectManagedSecret, s.Project.HandleCreateProjectManagedSecret)
	s.registerV1Operation(r, operationUpdateProjectManagedSecret, s.Project.HandleUpdateProjectManagedSecret)
	s.registerV1Operation(r, operationReplaceProjectManagedSecretValue, s.Project.HandleReplaceProjectManagedSecretValue)
	s.registerV1Operation(r, operationHoldProjectManagedSecret, s.Project.HandleHoldProjectManagedSecret)
	s.registerV1Operation(r, operationListProjectManagedSecretUses, s.Project.HandleListProjectManagedSecretUses)
	s.registerV1Operation(r, operationBeginProjectManagedSecretReveal, s.Project.HandleBeginProjectManagedSecretReveal)
	s.registerV1Operation(r, operationCompleteProjectManagedSecretReveal, s.Project.HandleCompleteProjectManagedSecretReveal)
	s.registerV1Operation(r, operationRevokeProjectManagedSecret, s.Project.HandleRevokeProjectManagedSecret)
	s.registerV1Operation(r, operationPromoteProject, s.Project.HandlePromoteProject)
	s.registerV1Operation(r, operationCancelProjectPromotion, s.Project.HandleCancelProjectPromotion)
	s.registerV1Operation(r, operationAttachProjectRoot, s.Project.HandleAttachProjectRoot)
	s.registerV1Operation(r, operationDetachProjectRoot, s.Project.HandleDetachProjectRoot)
	s.registerV1Operation(r, operationUpdateProjectRoot, s.Project.HandleUpdateProjectRoot)
	s.registerV1Operation(r, operationGetProjectSource, s.Sources.HandleGetProjectSource)
	s.registerV1Operation(r, operationGetSourceWorkspace, s.Sources.HandleGetSourceWorkspace)
	s.registerV1Operation(r, operationOpenEditorDocument, s.Sources.HandleOpenEditorDocument)
	s.registerV1Operation(r, operationReplaceEditorDocumentRetention, s.Sources.HandleReplaceEditorDocumentRetention)
	s.registerV1Operation(r, operationReadEditorDocumentStatuses, s.Sources.HandleReadEditorDocumentStatuses)
	s.registerV1Operation(r, operationReplaceEditorDocument, s.Sources.HandleReplaceEditorDocument)
	s.registerV1Operation(r, operationSyncEditorDocument, s.Sources.HandleSyncEditorDocument)
	s.registerV1Operation(r, operationResolveEditorDocumentConflict, s.Sources.HandleResolveEditorDocumentConflict)
	s.registerV1Operation(r, operationCreateEditorDocumentSnapshot, s.Sources.HandleCreateEditorDocumentSnapshot)
	s.registerV1Operation(r, operationListEditorDocumentChanges, s.Sources.HandleListEditorDocumentChanges)
	s.registerV1Operation(r, operationRevertEditorDocumentChange, s.Sources.HandleRevertEditorDocumentChange)
	s.registerV1Operation(r, operationSubmitEditorDocumentUpdate, s.Sources.HandleSubmitEditorDocumentUpdate)
	s.registerV1Operation(r, operationPublishEditorDocumentPresence, s.Sources.HandlePublishEditorDocumentPresence)
	s.registerV1Operation(r, operationLeaveEditorDocument, s.Sources.HandleLeaveEditorDocument)
	s.registerV1Operation(r, operationDiscardEditorDocument, s.Sources.HandleDiscardEditorDocument)
	s.registerV1Operation(r, operationReloadEditorDocument, s.Sources.HandleReloadEditorDocument)
	s.registerV1Operation(r, operationObserveEditorDocument, s.Sources.HandleObserveEditorDocument)
	s.registerV1Operation(r, operationSaveEditorDocument, s.Sources.HandleSaveEditorDocument)
	s.registerV1Operation(r, operationPreviewEditorSecretMark, s.Sources.HandlePreviewEditorSecretMark)
	s.registerV1Operation(r, operationMarkEditorSecret, s.Sources.HandleMarkEditorSecret)
	s.registerV1Operation(r, operationCreateProjectSourceEntry, s.Sources.SourceRequestHandler(toSourceOperation(operationCreateProjectSourceEntry), s.Sources.HandleCreateProjectSourceEntry))
	s.registerV1Operation(r, operationReplaceProjectSource, s.Sources.HandleReplaceProjectSource)
	s.registerV1Operation(r, operationMakeProjectSourceEditable, s.Sources.HandleMakeProjectSourceEditable)
	s.registerV1Operation(r, operationDeleteProjectSource, s.Sources.SourceRequestHandler(toSourceOperation(operationDeleteProjectSource), s.Sources.HandleDeleteProjectSource))
	s.registerV1Operation(r, operationGetProjectSourceRaw, s.Sources.HandleGetProjectSourceRaw)
	s.registerV1Operation(r, operationGetProjectSourceEditorConfig, s.Sources.HandleGetProjectSourceEditorConfig)
	s.registerV1Operation(r, operationBrowseProjectSource, s.Sources.HandleBrowseProjectSource)
	s.registerV1Operation(r, operationGetProjectSourceHistory, s.Sources.HandleGetProjectSourceHistory)
	s.registerV1Operation(r, operationUndoProjectSourceHistory, s.Sources.SourceRequestHandler(toSourceOperation(operationUndoProjectSourceHistory), s.Sources.HandleUndoProjectSourceHistory))
	s.registerV1Operation(r, operationRedoProjectSourceHistory, s.Sources.SourceRequestHandler(toSourceOperation(operationRedoProjectSourceHistory), s.Sources.HandleRedoProjectSourceHistory))
	s.registerV1Operation(r, operationRenameProjectSource, s.Sources.SourceRequestHandler(toSourceOperation(operationRenameProjectSource), s.Sources.HandleRenameProjectSource))
	s.registerV1Operation(r, operationCopyProjectSource, s.Sources.SourceRequestHandler(toSourceOperation(operationCopyProjectSource), s.Sources.HandleCopyProjectSource))
	s.registerV1Operation(r, operationGetProjectSourceIndex, s.Sources.HandleProjectSourceIndex)
	s.registerV1Operation(r, operationSearchProjectSource, s.Sources.HandleSearchProjectSource)
	s.registerV1Operation(r, operationResolveProjectSourceDefinition, s.Sources.HandleResolveProjectSourceDefinition)
	s.registerV1Operation(r, operationListProjectSourceSymbols, s.Sources.HandleListProjectSourceSymbols)
	s.registerV1Operation(r, operationListProjectSourceWalk, s.Sources.HandleListProjectSourceWalk)
	s.registerV1Operation(r, operationGetProjectSourceGitReview, s.Sources.HandleGetProjectSourceGitReview)
	s.registerV1Operation(r, operationResolveProjectSourceRevisions, s.Sources.HandleResolveProjectSourceRevisions)
	s.registerV1Operation(r, operationGetProjectSourceRevisionReview, s.Sources.HandleGetProjectSourceRevisionReview)
	s.registerV1Operation(r, operationGetProjectSourceWalkSummary, s.Sources.HandleGetProjectSourceWalkSummary)
	s.registerV1Operation(r, operationListProjectSourceVersions, s.Sources.HandleListProjectSourceVersions)
	s.registerV1Operation(r, operationRestoreProjectSourceVersion, s.Sources.HandleRestoreProjectSourceVersion)
	s.registerV1Operation(r, operationRestoreProjectSourceCommitState, s.Sources.HandleRestoreProjectSourceCommitState)
	s.registerV1Operation(r, operationGetFileBriefing, s.Sources.HandleGetFileBriefing)
	s.registerV1Operation(r, operationRequestFileBriefing, s.Sources.HandleRequestFileBriefing)
	s.registerV1Operation(r, operationCompleteProjectSourcePresentation, s.Sources.HandleCompleteProjectSourcePresentation)
	s.registerV1Operation(r, operationWithdrawProjectSourcePresentation, s.Sources.HandleWithdrawProjectSourcePresentation)
	s.registerV1Operation(r, operationListProjectSourceSeen, s.Sources.HandleListProjectSourceSeen)
	s.registerV1Operation(r, operationGetProjectSourceStorage, s.Sources.HandleGetProjectSourceStorage)
	s.registerV1Operation(r, operationGetProjectSecurity, s.Scan.HandleGetProjectSecurity)
	s.registerV1Operation(r, operationQueryProjectFindings, s.handleQueryProjectFindings)
	s.registerV1Operation(r, operationListProjectFindingIgnores, s.handleListProjectFindingIgnores)
	s.registerV1Operation(r, operationCreateProjectFindingIgnore, s.handleCreateProjectFindingIgnore)
	s.registerV1Operation(r, operationDeleteProjectFindingIgnore, s.handleDeleteProjectFindingIgnore)
	s.registerV1Operation(r, operationExportProjectFindings, s.handleExportProjectFindings)
	s.registerV1Operation(r, operationListProjectSourcePins, s.Sources.HandleListProjectSourcePins)
	s.registerV1Operation(r, operationCreateProjectSourcePin, s.Sources.HandleCreateProjectSourcePin)
	s.registerV1Operation(r, operationUpdateProjectSourcePin, s.Sources.HandleUpdateProjectSourcePin)
	s.registerV1Operation(r, operationDeleteProjectSourcePin, s.Sources.HandleDeleteProjectSourcePin)
	s.registerV1Operation(r, operationGetProjectSourceComparison, s.Sources.HandleGetProjectSourceComparison)
	s.registerV1Operation(r, operationCreateSourceView, s.Sources.HandleCreateSourceView)
	s.registerV1Operation(r, operationDigestSourceComparisons, s.Sources.HandleDigestSourceComparisons)
	s.registerV1Operation(r, operationGetSourceView, s.Sources.HandleGetSourceView)
	s.registerV1Operation(r, operationApplySourceViewIntent, s.Sources.HandleApplySourceViewIntent)
	s.registerV1Operation(r, operationReleaseSourceView, s.Sources.HandleReleaseSourceView)
	s.registerV1Operation(r, operationReplaceSourceViewportInterest, s.Sources.HandleReplaceSourceViewportInterest)
	s.registerV1Operation(r, operationReleaseSourceViewportInterest, s.Sources.HandleReleaseSourceViewportInterest)
	s.registerV1Operation(r, operationCreateSourcePresentation, s.Sources.HandleCreateSourcePresentation)
	s.registerV1Operation(r, operationReleaseSourcePresentation, s.Sources.HandleReleaseSourcePresentation)
	s.registerV1Operation(r, operationGetSourceViewRows, s.Sources.HandleGetSourceViewRows)
	s.registerV1Operation(r, operationLocateSourceView, s.Sources.HandleLocateSourceView)
	s.registerV1Operation(r, operationSearchSourceView, s.Sources.HandleSearchSourceView)
	s.registerV1Operation(r, operationGetProjectSourceAttribution, s.Sources.HandleGetProjectSourceAttribution)
}

func (s *Server) registerScanRoutes(r chi.Router) {
	s.registerV1Operation(r, operationListCodeScans, s.Scan.HandleListCodeScans)
	s.registerV1Operation(r, operationStartFullScan, s.Scan.HandleStartFullScan)
	s.registerV1Operation(r, operationGetCodeScan, s.Scan.HandleGetCodeScan)
	s.registerV1Operation(r, operationExportCodeScanSARIF, s.Scan.HandleExportCodeScanSARIF)
	s.registerV1Operation(r, operationQueryCodeScan, s.Scan.HandleQueryCodeScan)
	s.registerV1Operation(r, operationListScanners, s.Scan.HandleListScanners)
	s.registerV1Operation(r, operationListProjectScanners, s.Scan.HandleListProjectScanners)
	s.registerV1Operation(r, operationCreateScanner, s.Scan.HandleCreateScanner)
	s.registerV1Operation(r, operationListScannerCatalog, s.Scan.HandleListScannerCatalog)
	s.registerV1Operation(r, operationCheckScanners, s.Scan.HandleScannerCheck)
	s.registerV1Operation(r, operationReplaceScannerSlot, s.Scan.HandleReplaceScannerSlot)
	s.registerV1Operation(r, operationUpdateScanner, s.Scan.HandleUpdateScanner)
	s.registerV1Operation(r, operationUpdateProjectScanner, s.Scan.HandleUpdateProjectScanner)
	s.registerV1Operation(r, operationDeleteScanner, s.Scan.HandleDeleteScanner)
}

func (s *Server) registerWebResearchRoutes(r chi.Router) {
	s.registerV1Operation(r, operationGetWebResearchProviders, s.researchAdmin.GetProviders)
	s.registerV1Operation(r, operationGetWebResearchSettings, s.researchAdmin.GetSettings)
	s.registerV1Operation(r, operationUpdateWebResearchSettings, s.researchAdmin.UpdateSettings)
	s.registerV1Operation(r, operationUpdateWebResearchProvider, s.researchAdmin.UpdateProvider)
	s.registerV1Operation(r, operationReplaceWebResearchCredential, s.researchAdmin.SetCredential)
	s.registerV1Operation(r, operationDeleteWebResearchCredential, s.researchAdmin.DeleteCredential)
	s.registerV1Operation(r, operationTestWebResearchProvider, s.researchAdmin.TestProvider)
	s.registerV1Operation(r, operationGetWebResearchIndex, s.researchAdmin.GetIndex)
}

func (s *Server) registerMCPRoutes(r chi.Router) {
	s.registerV1Operation(r, operationListMcpRecipes, s.mcpAdmin.ListRecipes)
	s.registerV1Operation(r, operationListMcpProviders, s.mcpAdmin.ListProviders)
	s.registerV1Operation(r, operationCreateMcpProvider, s.mcpAdmin.CreateProvider)
	s.registerV1Operation(r, operationUpdateMcpProvider, s.mcpAdmin.UpdateProvider)
	s.registerV1Operation(r, operationDeleteMcpProvider, s.mcpAdmin.DeleteProvider)
	s.registerV1Operation(r, operationListMcpProviderTools, s.mcpAdmin.ListProviderTools)
	s.registerV1Operation(r, operationRefreshMcpProvider, s.mcpAdmin.RefreshProvider)
	s.registerV1Operation(r, operationStartMcpOAuth, s.mcpAdmin.StartOAuth)
	s.registerV1Operation(r, operationCompleteMcpOAuth, s.mcpAdmin.CompleteOAuth)
	s.registerV1Operation(r, operationCancelMcpOAuth, s.mcpAdmin.CancelOAuth)
	s.registerV1Operation(r, operationRevokeMcpOAuth, s.mcpAdmin.RevokeOAuth)
	s.registerV1Operation(r, operationCheckMcpProviders, s.mcpAdmin.CheckProviders)
}

func (s *Server) registerGitRoutes(r chi.Router) {
	s.registerV1Operation(r, operationListGitRepos, s.Git.HandleGitRepos)
	s.registerV1Operation(r, operationGetGitStatus, s.Git.HandleGitStatus)
	s.registerV1Operation(r, operationListGitChanges, s.Git.HandleGitChanges)
	s.registerV1Operation(r, operationListGitBranches, s.Git.HandleGitBranches)
	s.registerV1Operation(r, operationCheckoutGit, s.Git.HandleGitCheckout)
	s.registerV1Operation(r, operationCreateGitRepo, s.Git.HandleCreateGitRepo)
	s.registerV1Operation(r, operationCommitGit, s.Git.HandleGitCommit)
	s.registerV1Operation(r, operationStashGit, s.Git.HandleGitStash)
	s.registerV1Operation(r, operationDiscardGit, s.Git.HandleGitDiscard)
	s.registerV1Operation(r, operationPushGit, s.Git.HandleGitPush)
	s.registerV1Operation(r, operationPullGit, s.Git.HandleGitPull)
	s.registerV1Operation(r, operationDraftGitCommitMessage, s.Git.HandleGitCommitMessage)
	s.registerV1Operation(r, operationGetGitWorktree, s.Git.HandleGitWorktreeView)
	s.registerV1Operation(r, operationBindGitWorktree, s.Git.HandleGitWorktreeBind)
	s.registerV1Operation(r, operationLandGitWorktree, s.Git.HandleGitWorktreeLand)
	s.registerV1Operation(r, operationDeleteGitWorktree, s.Git.HandleDeleteGitWorktree)
}

func (s *Server) registerExtensionsRoutes(r chi.Router) {
	s.registerV1Operation(r, operationGetExtensionsCatalog, s.Extensions.HandleGetExtensionsCatalog)
	s.registerV1Operation(r, operationGetExtensionUnit, s.Extensions.HandleGetExtensionUnit)
	s.registerV1Operation(r, operationUpdateExtensionUnit, s.Extensions.HandleUpdateExtensionUnit)
	s.registerV1Operation(r, operationInstallExtensionPack, s.Extensions.HandleInstallExtensionPack)
	s.registerV1Operation(r, operationUpdateExtensionPack, s.Extensions.HandleUpdateExtensionPack)
	s.registerV1Operation(r, operationDeleteExtensionPack, s.Extensions.HandleDeleteExtensionPack)
	s.registerV1Operation(r, operationUpdateExtensionConfiguration, s.Extensions.HandleUpdateExtensionConfiguration)
	s.registerV1Operation(r, operationGetExtensionPackUpdate, s.Extensions.HandleGetExtensionPackUpdate)
	s.registerV1Operation(r, operationApplyExtensionPackUpdate, s.Extensions.HandleApplyExtensionPackUpdate)
	s.registerV1Operation(r, operationReloadExtensionPack, s.Extensions.HandleReloadExtensionPack)
	s.registerV1Operation(r, operationApplyExtensionPackProfile, s.Extensions.HandleApplyExtensionPackProfile)
	s.registerV1Operation(r, operationInstallExtensionMetaPack, s.Extensions.HandleInstallExtensionMetaPack)
	s.registerV1Operation(r, operationUpdateExtensionMetaPack, s.Extensions.HandleUpdateExtensionMetaPack)
	s.registerV1Operation(r, operationDeleteExtensionMetaPack, s.Extensions.HandleDeleteExtensionMetaPack)
	s.registerV1Operation(r, operationApplyExtensionMetaPack, s.Extensions.HandleApplyExtensionMetaPack)
	s.registerV1Operation(r, operationGetExtensionSuggestions, s.Extensions.HandleGetExtensionSuggestions)
	s.registerV1Operation(r, operationAcceptExtensionSuggestions, s.Extensions.HandleAcceptExtensionSuggestions)
}

func (s *Server) registerHostResourceRoutes(r chi.Router) {
	s.registerV1Operation(r, operationGetHostResources, s.handleGetHostResources)
	s.registerV1Operation(r, operationRefreshHostResources, s.handleRefreshHostResources)
	s.registerV1Operation(r, operationUpdateHostResource, s.handleUpdateHostResource)
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

func (s *Server) registerSettingsRoutes(r chi.Router) {
	s.registerV1Operation(r, operationGetApprovalsSettings, s.Settings.HandleGetApprovals)
	s.registerV1Operation(r, operationUpdateApprovalsSettings, s.Settings.HandleUpdateApprovals)
	s.registerV1Operation(r, operationListApprovalGrants, s.Capabilities.HandleListApprovalGrants)
	s.registerV1Operation(r, operationCreateApprovalGrant, s.Capabilities.HandleCreateApprovalGrant)
	s.registerV1Operation(r, operationListApprovalAsks, s.Capabilities.HandleListApprovalAsks)
	s.registerV1Operation(r, operationResolveSocketGrant, s.Capabilities.HandleResolveSocketGrant)
	s.registerV1Operation(r, operationRevokeApprovalGrants, s.Capabilities.HandleRevokeApprovalGrants)
	s.registerV1Operation(r, operationGetElevatedAccess, s.Capabilities.HandleGetElevatedAccess)
	s.registerV1Operation(r, operationRevokeElevatedAccess, s.Capabilities.HandleRevokeElevatedAccess)
	s.registerV1Operation(r, operationListDetectionPacks, s.Scan.HandleListDetectionPacks)
	s.registerV1Operation(r, operationUpdateDetectionPack, s.Scan.HandleUpdateDetectionPack)
	s.registerV1Operation(r, operationImportDetectionPack, s.Scan.HandleImportDetectionPack)
	s.registerV1Operation(r, operationDeleteDetectionPack, s.Scan.HandleDeleteDetectionPack)
	s.registerV1Operation(r, operationGetLimitsSettings, s.Settings.HandleGetLimits)
	s.registerV1Operation(r, operationUpdateLimitsSettings, s.Settings.HandleUpdateLimits)
	s.registerV1Operation(r, operationGetReviewSettings, s.Settings.HandleGetReview)
	s.registerV1Operation(r, operationUpdateReviewSettings, s.Settings.HandleUpdateReview)
	s.registerV1Operation(r, operationGetFileSummariesSettings, s.Settings.HandleGetFileSummariesSettings)
	s.registerV1Operation(r, operationUpdateFileSummariesSettings, s.Settings.HandleUpdateFileSummariesSettings)
	s.registerV1Operation(r, operationGetPowerSettings, s.Settings.HandleGetPowerSettings)
	s.registerV1Operation(r, operationUpdatePowerSettings, s.Settings.HandleUpdatePowerSettings)
	s.registerV1Operation(r, operationGetVerifySettings, s.Settings.HandleGetVerify)
	s.registerV1Operation(r, operationUpdateVerifySettings, s.Settings.HandleUpdateVerify)
	s.registerV1Operation(r, operationDismissVerifySettings, s.Settings.HandleDismissVerify)
	s.registerV1Operation(r, operationGetSecurityScannersSettings, s.Settings.HandleGetSecurityScannersSettings)
	s.registerV1Operation(r, operationUpdateSecurityScannersSettings, s.Settings.HandleUpdateSecurityScannersSettings)
	s.registerV1Operation(r, operationGetPricingSettings, s.Settings.HandleGetPricingSettings)
	s.registerV1Operation(r, operationUpdatePricingSettings, s.Settings.HandleUpdatePricingSettings)
	s.registerV1Operation(r, operationRefreshPricingSource, s.Settings.HandleRefreshPricingSource)
	s.registerV1Operation(r, operationGetTrustSettings, s.Project.HandleGetTrustSettings)
	s.registerV1Operation(r, operationUpdateTrustSettings, s.Project.HandleUpdateTrustSettings)
}
