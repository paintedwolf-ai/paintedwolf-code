package api

import (
	"context"
	"log/slog"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/capabilityadmin"
	"github.com/lycaon/lycaon/internal/api/extensionadmin"
	"github.com/lycaon/lycaon/internal/api/gitadmin"
	"github.com/lycaon/lycaon/internal/api/historyadmin"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/mcpadmin"
	"github.com/lycaon/lycaon/internal/api/modeladmin"
	"github.com/lycaon/lycaon/internal/api/projectadmin"
	"github.com/lycaon/lycaon/internal/api/promptadmin"
	"github.com/lycaon/lycaon/internal/api/researchadmin"
	"github.com/lycaon/lycaon/internal/api/scanadmin"
	"github.com/lycaon/lycaon/internal/api/searchadmin"
	"github.com/lycaon/lycaon/internal/api/sessionadmin"
	"github.com/lycaon/lycaon/internal/api/sessionview"
	"github.com/lycaon/lycaon/internal/api/settingsadmin"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/api/workflowadmin"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/hitl"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func NewServer(deps Dependencies, logger *slog.Logger, token string) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	requireDependencies(deps)
	s := &Server{
		responses:      httpio.Responder{Logger: logger, Notices: deps.Core.UserNotices},
		apiToken:       strings.TrimSpace(token),
		sessionStore:   deps.Core.Store,
		personActions:  deps.Core.PersonActions,
		sessions:       deps.Core.Sessions,
		fileBriefings:  deps.Source.FileBriefings,
		events:         deps.Host.Events,
		eventPublisher: deps.Host.EventPublisher,
		presence:       deps.Host.Presence,
		storagePaths: storagePaths{
			dataDir:          strings.TrimSpace(deps.Storage.DataDir),
			storePath:        strings.TrimSpace(deps.Storage.StorePath),
			workerBranchRoot: strings.TrimSpace(deps.Storage.WorkerBranchRoot),
			workerSeedRoot:   strings.TrimSpace(deps.Storage.WorkerSeedRoot),
		},
		health: health{
			storeRevision:      deps.Storage.StoreRevision,
			previousAppVersion: strings.TrimSpace(deps.Storage.PreviousAppVersion),
			minDenVersion:      strings.TrimSpace(deps.Storage.MinDenVersion),
		},
	}
	s.Admin.SessionView = sessionview.New(sessionview.Projector{Workflows: deps.Workflow.Workflows, Store: deps.Core.Store, Sessions: deps.Core.Sessions, Projects: deps.Core.Projects})
	s.Admin.Git = gitadmin.New(&s.responses, gitadmin.Deps{
		Board: deps.Workflow.Board, CommitDrafter: deps.Providers.CommitDrafter, LLMService: deps.Providers.LLM, ProjectRegistry: deps.Core.Projects,
		RepoSetCache: deps.Workflow.RepoSetCache, SessionStore: deps.Core.Store, Sessions: deps.Core.Sessions, DataDir: s.dataDir,
	})
	var attachmentStore func(context.Context, string) (blobstore.Store, bool)
	var tryRunPromotion func(context.Context, string)
	s.Sources = sourceapi.New(&s.responses, &s.background, sourceOperations, sourceapi.Deps{
		Git: &s.Admin.Git, MutationGate: deps.Core.MutationGate,
		CatalogSnapshot: deps.Source.CatalogSnapshot, WatchNeedsSeed: deps.Source.WatchNeedsSeed,
		EditorClients: deps.Source.EditorClients, EditorDocuments: deps.Source.EditorDocuments, Events: deps.Host.Events,
		FileBriefings: deps.Source.FileBriefings, FileOperations: deps.Source.FileOperations, ManagedSecrets: deps.Approvals.ManagedSecrets,
		ProjectRegistry: deps.Core.Projects, ScanCadence: deps.Scans.ScanCadence,
		SecretSpans: deps.Approvals.SecretSpans, SessionStore: deps.Core.Store, SourceInventory: deps.Source.SourceInventory,
		SourceLedger: deps.Source.SourceLedger, SourceMutations: deps.Source.SourceMutations, VisualStore: deps.Source.VisualStore,
		Workers: deps.Workflow.Workers, AttachmentStore: func(ctx context.Context, id string) (blobstore.Store, bool) { return attachmentStore(ctx, id) }, TryRunPromotion: func(ctx context.Context, id string) { tryRunPromotion(ctx, id) },
	})
	s.Search = searchadmin.New(&s.responses, searchadmin.Dependencies{Database: deps.Core.Database, Projects: deps.Core.Projects, Rerank: deps.Providers.Rerank, SourceMutations: deps.Source.SourceMutations, ChatAffiliation: s.Sources.Workspace.UserSourceChatAffiliation, WriteSourceError: s.Sources.Workspace.WriteProjectSourceError})
	s.Admin.Prompt = promptadmin.New(&s.responses, &s.background, promptadmin.Deps{
		DataDir: s.dataDir, EventPublisher: deps.Host.EventPublisher, HintConfig: deps.Source.HintConfig,
		ManagedSecrets: deps.Approvals.ManagedSecrets, Projects: deps.Core.Projects, Store: deps.Core.Store, Sessions: deps.Core.Sessions,
		VisualStore: deps.Source.VisualStore, Sources: &s.Sources, Video: deps.Source.Video,
	})
	s.Admin.SessionAdmin = sessionadmin.New(&s.responses, &s.background, sessionadmin.Deps{
		Checkpoints: deps.Approvals.Checkpoints, EventPublisher: deps.Host.EventPublisher, Events: deps.Host.Events,
		FileAgeWarmer: deps.Source.FileAgeWarmer, Invocations: deps.Core.Invocations, LLMService: deps.Providers.LLM, Preview: deps.Host.Preview,
		ProgressStore: deps.Source.ProgressStore, Projects: deps.Core.Projects, ProjectRules: deps.Source.ProjectRules, Store: deps.Core.Store,
		Sessions: deps.Core.Sessions, Workers: deps.Workflow.Workers, Workflows: deps.Workflow.Workflows, Settings: deps.Core.Settings,
		Sources: &s.Sources, SessionView: &s.Admin.SessionView, Git: &s.Admin.Git, Prompt: &s.Admin.Prompt,
	})
	s.Admin.Workflow = workflowadmin.New(&s.responses, &s.background, workflowadmin.Deps{
		Workflows: deps.Workflow.Workflows, Catalog: deps.Workflow.WorkflowCatalog, Runs: deps.Workflow.WorkflowRuns,
		Composer: deps.Workflow.WorkflowComposer, Persister: deps.Workflow.WorkflowPersister, Blueprints: deps.Workflow.Blueprints,
		Orchestrator: deps.Workflow.Orchestrator, EventPublisher: deps.Host.EventPublisher, ManagedSecrets: deps.Approvals.ManagedSecrets,
		Projects: deps.Core.Projects, Scans: deps.Scans.ScanCoordinator, Store: deps.Core.Store, Sessions: deps.Core.Sessions,
		VisualStore: deps.Source.VisualStore, Workers: deps.Workflow.Workers, SessionAdmin: &s.Admin.SessionAdmin, SessionView: &s.Admin.SessionView,
	})
	s.Admin.Scan = scanadmin.New(&s.responses, scanadmin.Deps{
		DetectionPacksDir: s.dataDir, PublishDetections: deps.Scans.PublishDetections, GateRepeatLedger: deps.Scans.GateRepeatLedger,
		Registry: deps.Scans.ScannerRegistry, ModuleRoot: deps.Storage.ModuleRoot, Projects: deps.Core.Projects, Cadence: deps.Scans.ScanCadence,
		Coordinator: deps.Scans.ScanCoordinator, Sessions: deps.Core.Sessions, Settings: deps.Core.Settings,
	})
	s.Admin.Extensions = extensionadmin.New(&s.responses, &s.background, extensionadmin.Deps{
		Owner: &extensionstate.Owner{Views: deps.Extensions.ExtensionViews, Scanners: deps.Extensions.ExtensionScanners,
			Journal: deps.Extensions.ExtensionJournal}, Runtime: deps.Extensions.Contributions, Events: deps.Host.Events, MCPRegistry: deps.External.MCP,
		ModuleRoot: deps.Storage.ModuleRoot, Projects: deps.Core.Projects, Store: deps.Core.Store, Sessions: deps.Core.Sessions,
		Settings: deps.Core.Settings, Workflow: &s.Admin.Workflow, Prompt: &s.Admin.Prompt, Scan: &s.Admin.Scan,
	})
	s.Admin.Project = projectadmin.New(&s.responses, &s.background, projectadmin.Deps{
		Extensions: &s.Admin.Extensions, Board: deps.Workflow.Board, Database: deps.Core.Database, DataDir: s.dataDir, Events: deps.Host.Events, LLMService: deps.Providers.LLM,
		SecretIgnores: deps.Approvals.SecretIgnores, ManagedSecrets: deps.Approvals.ManagedSecrets, MutationGate: deps.Core.MutationGate, Registry: deps.Core.Projects,
		ProjectRules: deps.Source.ProjectRules, ScanCadence: deps.Scans.ScanCadence, Store: deps.Core.Store, Sessions: deps.Core.Sessions,
		Settings: deps.Core.Settings, WorkerSeedRoot: s.workerSeedRoot, WorkerBranchRoot: s.workerBranchRoot,
		Workers: deps.Workflow.Workers, Sources: &s.Sources, Git: &s.Admin.Git,
	})
	attachmentStore = s.Admin.Prompt.Attachments.AttachmentStore
	tryRunPromotion = s.Admin.Project.Promotion.TryRunPromotion
	s.Admin.Project.Removal.InitProjectRemoval()
	s.Admin.Capabilities = capabilityadmin.New(&s.responses, capabilityadmin.Deps{
		HostResources: deps.Host.HostResources,
		Authority:     deps.Approvals.Authority, Gate: deps.Approvals.ApprovalGate, Checkpoints: deps.Approvals.Checkpoints, Events: deps.Host.Events,
		LLMService: deps.Providers.LLM, ManagedSecrets: deps.Approvals.ManagedSecrets, Projects: deps.Core.Projects, Store: deps.Core.Store,
		Settings: deps.Core.Settings,
	})
	s.Admin.Settings = settingsadmin.New(&s.responses, settingsadmin.Deps{
		Pricing: deps.Host.Pricing, Power: deps.Host.HostPower, Events: deps.Host.Events, Projects: deps.Core.Projects,
		Sessions: deps.Core.Sessions, Service: deps.Core.Settings, Sources: &s.Sources,
	})
	s.Admin.modelAdmin = modeladmin.New(deps.Providers.LLM, deps.Core.Projects, deps.Host.Events, &s.responses)
	s.Admin.historyAdmin = historyadmin.New(deps.External.HistoryStorage, &s.responses)
	s.Admin.mcpAdmin = mcpadmin.New(deps.External.MCP, deps.Core.Projects, &s.responses)
	s.Admin.researchAdmin = researchadmin.New(&s.responses, researchadmin.Deps{
		Runtime: deps.External.WebResearch, Discoverer: deps.External.WebDiscoverer, Index: deps.External.WebIndex, Events: deps.Host.Events,
	})
	s.observeDependencies(deps)
	s.router = chi.NewRouter()
	s.setupMiddleware()
	composeDomains(s, deps)
	s.setupRoutes()
	return s
}

// requireDependencies refuses to build a server without a service the host
// builds on every boot; the route families validate their own.
func requireDependencies(deps Dependencies) {
	httpio.RequireDependencies("api",
		httpio.Required{Name: "AgentPresence", Present: deps.Source.AgentPresence != nil},
		httpio.Required{Name: "Board", Present: deps.Workflow.Board != nil},
		httpio.Required{Name: "Checkpoints", Present: deps.Approvals.Checkpoints != nil},
		httpio.Required{Name: "CostTracker", Present: deps.Providers.CostTracker != nil},
		httpio.Required{Name: "Database", Present: deps.Core.Database != nil},
		httpio.Required{Name: "Delegations", Present: deps.Workflow.Delegations != nil},
		httpio.Required{Name: "EventPublisher", Present: deps.Host.EventPublisher != nil},
		httpio.Required{Name: "Events", Present: deps.Host.Events != nil},
		httpio.Required{Name: "HostIdentity", Present: deps.Host.HostIdentity.HostID != ""},
		httpio.Required{Name: "HostResources", Present: deps.Host.HostResources != nil},
		httpio.Required{Name: "LLM", Present: deps.Providers.LLM != nil},
		httpio.Required{Name: "ProgressStore", Present: deps.Source.ProgressStore != nil},
		httpio.Required{Name: "Projects", Present: deps.Core.Projects != nil},
		httpio.Required{Name: "ScanCadence", Present: deps.Scans.ScanCadence != nil},
		httpio.Required{Name: "Sessions", Present: deps.Core.Sessions != nil},
		httpio.Required{Name: "Settings.Approvals", Present: deps.Core.Settings != nil && deps.Core.Settings.Approvals != nil},
		httpio.Required{Name: "Store", Present: deps.Core.Store != nil},
		httpio.Required{Name: "VisualStore", Present: deps.Source.VisualStore != nil},
		httpio.Required{Name: "WorkerCancel", Present: deps.Workflow.WorkerCancel != nil},
		httpio.Required{Name: "Workers", Present: deps.Workflow.Workers != nil},
	)
}

// observeDependencies registers the server's reactions to its dependencies'
// changes. Each hook captures a handler at its final address in s.
func (s *Server) observeDependencies(deps Dependencies) {
	deps.Approvals.SecretIgnores.Changed = s.Sources.Editor.RefreshProjectSecretScreens
	deps.Approvals.ManagedSecrets.AddScreeningInvalidationObserver(s.Sources.Editor.RefreshProjectSecretScreens)
	if installer, ok := deps.Approvals.Checkpoints.(hitl.ApprovalAuthorityInstallerSetter); ok {
		installer.SetApprovalAuthorityInstaller(s.Admin.Capabilities.Installation)
	}
	deps.Source.EditorDocuments.SetOnChange(func(ctx context.Context, change editordoc.Change) {
		s.Routes.Activity.EditorDocumentChanged(ctx, change)
		d := change.Document
		_ = deps.Host.Events.Publish(ctx, wire.EventTopicEditorDocument,
			events.PublishKey{Project: d.ProjectID, Facet: d.ID},
			s.Sources.Editor.EditorDocumentEventDTO(ctx, d, change.ContentChanged))
	})
}
