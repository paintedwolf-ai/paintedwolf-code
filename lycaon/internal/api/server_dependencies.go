package api

import (
	"context"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/api/capabilityadmin"
	"github.com/lycaon/lycaon/internal/api/extensionadmin"
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
)

// Dependencies are the host-supplied services the API routes over.
type Dependencies struct {
	Core       CoreDependencies
	Providers  ProvidersDependencies
	Host       HostDependencies
	Storage    StorageDependencies
	Approvals  ApprovalsDependencies
	Scans      ScansDependencies
	Workflow   WorkflowDependencies
	Source     SourceDependencies
	Extensions ExtensionsDependencies
	External   ExternalDependencies
	Harness    HarnessDependencies
}

type CoreDependencies struct {
	Database      db.Handle
	Store         session.Store
	PersonActions *personactions.Store
	Projects      project.Registry
	Sessions      *session.Host
	Settings      *settings.Service
	UserNotices   *usernotice.Catalog
	Invocations   invocation.Recorder
	MutationGate  *project.MutationGate
}

type ProvidersDependencies struct {
	LLM           *llm.Service
	CostTracker   cost.CostTracker
	Rerank        decide.Reranker
	CommitDrafter compaction.Summarizer
}

type HostDependencies struct {
	Events         events.ReplayHub
	EventPublisher *events.Publisher
	Presence       *events.Presence
	HostIdentity   hostidentity.Identity
	HostResources  *hostresources.Service
	HostPower      *hostpower.Controller
	Pricing        *settings.PricingHost
	Preview        *preview.Controller
	PreflightEnv   preflight.Env
	Attention      *attention.Source
}

type StorageDependencies struct {
	DataDir            string
	StorePath          string
	WorkerBranchRoot   string
	WorkerSeedRoot     string
	ModuleRoot         string
	StoreRevision      uint64
	PreviousAppVersion string
	MinDenVersion      string
}

type ApprovalsDependencies struct {
	SecretIgnores  *projectignore.SecretService
	ManagedSecrets *secretcap.Service
	SecretSpans    *secretspan.Screener
	Checkpoints    hitl.CheckpointManager
	ApprovalGate   hitl.ApprovalGate
	Authority      capabilityadmin.Authority
}

type ScansDependencies struct {
	ScanCoordinator   scan.ScanCoordinator
	ScannerRegistry   scan.CodeScannerRegistry
	ScanCadence       *scancadence.Service
	GateRepeatLedger  *approvalstate.GateRepeatLedger
	PublishDetections func(*detectionpack.Matcher)
}

type WorkflowDependencies struct {
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
}

type SourceDependencies struct {
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
	Video           promptattach.VideoDecoder
	SourceInventory sourceapi.InventoryService
	CatalogSnapshot sourceapi.CatalogSnapshotFunc
	WatchNeedsSeed  func(rootPath string) bool
}

type ExtensionsDependencies struct {
	ExtensionViews    *catalogview.Cache
	ExtensionScanners extpacks.ScannerRequirementChecker
	ExtensionJournal  extensionstate.Journal
	Contributions     extensionadmin.ContributionRuntime
}

type ExternalDependencies struct {
	MCP            *mcp.Runtime
	WebResearch    webresearch.Runtime
	WebDiscoverer  webresearch.DirectDiscovererFactory
	WebIndex       *webindex.Store
	HistoryStorage *historyretention.Service
}

type HarnessDependencies struct {
	ManualLLM      *llm.ManualProvider
	HarnessWorkers *harnessfixture.Workers
}

// Route families cross-reference each other through fixed addresses in s, so construction order is safe.
