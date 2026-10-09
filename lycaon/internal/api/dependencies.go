package api

import (
	"context"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/api/capabilityadmin"
	"github.com/lycaon/lycaon/internal/api/extensionadmin"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/attention"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/browser/preview"
	"github.com/lycaon/lycaon/internal/catalogview"
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
	"github.com/lycaon/lycaon/internal/mcp"
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
