package app

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/capabilityadmin"
	"github.com/lycaon/lycaon/internal/api/extensionadmin"
	"github.com/lycaon/lycaon/internal/app/interactions"
	"github.com/lycaon/lycaon/internal/app/observations"
	"github.com/lycaon/lycaon/internal/app/readiness"
	"github.com/lycaon/lycaon/internal/attention"
	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/commandinvoke"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/destconfig"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/fileops"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/harnessfixture"
	"github.com/lycaon/lycaon/internal/historyretention"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/people/personactions"
	"github.com/lycaon/lycaon/internal/pkgregistry"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectliveness"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourcerewind"
	"github.com/lycaon/lycaon/internal/usernotice"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workernotice"
	wire "github.com/lycaon/lycaon/pkg/api"
	"os"
)

func (b *serveBuilder) wireRuntimeServices() error {
	b.events.Publisher.Board = b.boards.Snapshot
	b.interactions = *interactions.New(b.events.Publisher, b.startup.resources)
	if err := b.interactions.Build(b.sessions.Manager.Processes.HandleCommandCompletion, b.sessions.Manager.Processes.HandleCommandRefusal, b.sessions.Manager.Processes.HandleHeldCallSettled, b.sessions.Manager.Resources.RegisterCleanup); err != nil {
		return err
	}
	b.execution.Host.Commands.SetBackgroundRegistry(b.interactions.Processes)
	b.sessions.Manager.SetBackgroundRegistry(b.interactions.Processes)
	b.sessions.Manager.SetHeldCalls(b.interactions.Calls)
	b.sessions.Manager.SetPageRegistry(b.interactions.Pages)
	if err := b.interactions.RegisterTools(b.execution.Host.Registry, b.boards.BrowserPool); err != nil {
		return err
	}
	return b.wireRuntimeObservers()
}

func (b *serveBuilder) wireRuntimeObservers() error {
	b.sessions.Manager.SetEventPublisher(b.events.Publisher)
	b.delegations.Queue.SetEventPublisher(b.events.Publisher)
	releaseObservers := observations.Bind(b.events.Publisher, b.storage.Sessions, b.boards.Progress, b.workflows.Store.Runs, b.sessions.Manager.Coordinator.ProgressClosure.AfterWrite, b.boards.RepoProvider, b.execution.Host.Survey.InvalidateFileAge, b.providers.Service)
	b.startup.resources.Track("runtime-observers", 22, releaseObservers)
	return nil
}

// wireServer builds the API server once over the host's services, then
// registers the recoveries and hooks that call into its handlers.
func (b *serveBuilder) wireServer() error {
	extensionJournal := extensionstate.NewSQLJournal(b.storage.Database)
	deps := api.Dependencies{Core: api.CoreDependencies{
		Database: b.storage.Database, Store: b.storage.Sessions, PersonActions: personactions.New(b.storage.Database), Projects: b.storage.Projects, Sessions: b.sessions.Manager, Settings: b.settings.Service,
		Invocations: b.sessions.Invocations, MutationGate: project.NewMutationGate()}, Providers: api.ProvidersDependencies{LLM: b.providers.Service, CostTracker: b.providers.Costs, Rerank: b.decisions.Rerank}, Host: api.HostDependencies{
		Events: b.events.Hub, EventPublisher: b.events.Publisher, Presence: b.events.Presence, HostIdentity: b.identity.Host,
		HostResources: b.settings.HostResources, HostPower: b.settings.Power, Pricing: b.providers.Pricing, Preview: b.interactions.Preview,
		PreflightEnv: readiness.New(b.providers.Service, b.decisions.Decider, b.processes.Path).Environment()}, Storage: api.StorageDependencies{
		DataDir: b.storage.Directory, StorePath: b.storage.Path, WorkerBranchRoot: b.delegations.BranchRoot, WorkerSeedRoot: b.delegations.SeedRoot,
		ModuleRoot: b.catalog.ModuleRoot, StoreRevision: b.storage.Revision, MinDenVersion: os.Getenv("LYCAON_MIN_DEN_VERSION")}, Approvals: api.ApprovalsDependencies{
		SecretIgnores: b.security.Ignores, ManagedSecrets: b.security.Capabilities, SecretSpans: b.security.Spans,
		Checkpoints: b.sessions.Checkpoints, ApprovalGate: b.execution.Host.Authority.ApprovalGate(),
		Authority: capabilityadmin.Authority{
			DirectIP: b.security.DirectIP, GrantedPaths: b.sessions.GrantedPath, Listen: b.sessions.SandboxListen,
			Loopback: b.sessions.SandboxLoopback, ReadPaths: b.sessions.SandboxReadPath, WriteRoots: b.sessions.SandboxWriteRoot,
			Sockets: b.security.Sockets, ChatGrants: chatGrantLedger(b.sessions.Checkpoints), Vault: b.security.Unlocks,
		}}, Scans: api.ScansDependencies{
		ScanCoordinator: b.scanning.Coordinator, ScannerRegistry: b.scanning.Registry, ScanCadence: b.scanning.Cadence,
		GateRepeatLedger: b.sessions.GateRepeat, PublishDetections: b.security.Detections.Publish}, Workflow: api.WorkflowDependencies{
		Workflows: b.workflows.Manager, WorkflowCatalog: b.workflows.Resolver,
		WorkflowRuns: b.workflows.Store, WorkflowComposer: b.workflows.Composer, WorkflowPersister: b.workflows.Persister,
		Blueprints: b.workflows.Blueprints, Orchestrator: b.server.Orchestrator, Delegations: b.delegations.Manager, Workers: b.delegations.Queue,
		WorkerCancel: b.delegations.Cancel, Board: b.boards.Snapshot, RepoSetCache: b.git.repoSets}, Source: api.SourceDependencies{
		AgentPresence: b.events.Agents, ProjectRules: b.workflows.ProjectRules, HintConfig: b.execution.Hints,
		FileAgeWarmer: b.execution.Host.Survey.WarmFileAge, VisualStore: b.boards.Visual, ProgressStore: b.boards.Progress,
		Video: videoDecoder(b)}, Extensions: api.ExtensionsDependencies{
		ExtensionViews: b.catalog.ViewCache, ExtensionJournal: extensionJournal}, External: api.ExternalDependencies{
		MCP: b.server.MCP, WebResearch: b.boards.WebRuntime, WebDiscoverer: b.boards.WebDiscoverer, WebIndex: b.storage.WebIndex}, Harness: api.HarnessDependencies{ManualLLM: b.providers.Manual, HarnessWorkers: b.worker.harnessWorkers}}
	if b.security.Authority != nil {
		deps.Approvals.Authority.AuthzRecorder = b.security.Authority.Recorder
		deps.Approvals.Authority.ApprovalDecisions = b.security.Authority.Store
	}
	b.server.ProjectLiveness = projectliveness.New(projectliveness.Config{
		Sessions: b.storage.Sessions,
		Handler: appProjectLifecycleHandler{
			activate: func(ctx context.Context, projectID string) error {
				if b.server.Server != nil {
					b.server.Server.Sources.Watch.ScheduleSourceWatch(ctx, projectID)
				}
				return nil
			},
			park: func(ctx context.Context, projectID string) error {
				sourcefeed.StopProjectWatch(ctx, projectID)
				sourcecatalog.Process().Trees.SuspendProjectStores(projectID)
				return nil
			},
		},
	})
	b.startup.resources.Track("project-liveness", 85, func(context.Context) error {
		b.server.ProjectLiveness.Close()
		return nil
	})
	deps.Source.ProjectLiveness = b.server.ProjectLiveness
	b.sessions.Manager.Runner.Execution.SetProjectLiveness(b.server.ProjectLiveness)
	b.sessions.Manager.Runner.Execution.SetMutationGate(deps.Core.MutationGate)
	prev, ok, err := db.ReadBootPreviousAppVersion(b.startup.ctx, b.storage.Database)
	if err != nil {
		return fmt.Errorf("previous app version: %w", err)
	} else if ok {
		deps.Storage.PreviousAppVersion = prev
	}
	userNoticeCfg, err := usernotice.LoadEffectiveUserNotices(extpacks.Active())
	if err != nil {
		return fmt.Errorf("user notices: %w", err)
	}
	b.server.UserNotices = usernotice.NewCatalog(userNoticeCfg)
	deps.Core.UserNotices = b.server.UserNotices
	b.delegations.Queue.SetFailureRenderer(workernotice.NewRenderer(b.server.UserNotices))
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		return fmt.Errorf("extension subsystem owner: %w", err)
	}
	deps.Extensions.ExtensionScanners = scan.RequirementChecker{ModuleRoot: b.catalog.ModuleRoot, HomeDir: homeDir}
	b.server.HistoryStorage = historyretention.New(b.storage.Database, b.storage.Path, b.boards.Visual)
	deps.External.HistoryStorage = b.server.HistoryStorage
	// HTTP and SSE share one attention source.
	deps.Host.Attention = &attention.Source{
		Sessions:    b.storage.Sessions,
		Checkpoints: b.sessions.Checkpoints,
		Asks:        b.workflows.Manager.Asks,
		Finishes:    b.storage.Sessions,
		Projects:    attention.RegistryNamer{Registry: b.storage.Projects},
	}
	b.events.Publisher.Attention = deps.Host.Attention
	if manager, ok := b.sessions.Checkpoints.(*hitl.Checkpoints); ok {
		if err := b.registerRecovery(bootrecovery.Entry{
			Name: "approval-operations", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
			Run: manager.Authority.RecoverApprovalOperations,
		}); err != nil {
			return err
		}
		// Chat approvals outlive restart; they replay after unfinished approvals roll back.
		if err := b.registerRecovery(bootrecovery.Entry{
			Name: "chat-grants", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
			After: []string{"approval-operations"},
			Run:   manager.Authority.RestoreChatGrants,
		}); err != nil {
			return err
		}
	}
	if err := wireFileBriefings(b, &deps); err != nil {
		return err
	}
	if err := wireSourceEditing(b, &deps); err != nil {
		return err
	}
	b.server.Server = api.NewServer(deps, b.startup.logger, b.identity.Token)
	watch := b.server.Server.Sources.Watch
	b.startup.resources.Track("api-source-watches", 22, func(ctx context.Context) error { watch.Stop(); return watch.Wait(ctx) })
	return registerServerHooks(b, extensionJournal)
}

// chatGrantLedger exposes the checkpoint manager's chat approvals to revoke.
func chatGrantLedger(checkpoints hitl.CheckpointManager) capabilityadmin.ChatGrantLedger {
	if manager, ok := checkpoints.(*hitl.Checkpoints); ok {
		return manager.Authority
	}
	return nil
}

// registerServerHooks registers the recoveries and session hooks that call
// into the constructed server's handlers.
func registerServerHooks(b *serveBuilder, extensionJournal *extensionstate.SQLJournal) error {
	// Publish execution failures before queued follow-up work can delay the caller.
	b.sessions.Manager.Runner.Turns.SetFailureSink(b.server.Server.Admin.Prompt.Execution.PublishTurnFailure)
	b.sessions.Manager.Admission.SetPromotion(b.server.Server.Admin.Project.Promotion.TryRunPromotion)
	b.sessions.Manager.Runner.Settlement.SetSandboxReconcile(b.server.Server.Admin.Project.Sandboxes.ScheduleProjectSandboxReconcile)
	// Source views addressed by a chat end with it.
	if err := b.sessions.Manager.Resources.RegisterDisposal("source-views", 60, func(_ context.Context, sessionID string) error {
		b.server.Server.Sources.Views.ReleaseChatSourceViews(sessionID)
		return nil
	}); err != nil {
		return err
	}
	// Serve recovery begins after runtime gates are sealed.
	if err := b.registerRecovery(bootrecovery.Entry{
		Name: "prompt-submissions", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseServe,
		Run: b.server.Server.Admin.Prompt.Execution.RecoverPromptSubmissions,
	}); err != nil {
		return err
	}
	if err := b.registerRecovery(bootrecovery.Entry{
		Name: "project-promotions", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
		Run: b.server.Server.Admin.Project.Promotion.RecoverPromotions,
	}); err != nil {
		return err
	}
	if err := b.registerRecovery(bootrecovery.Entry{
		Name: "source-file-requests", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
		After: []string{"source-mutations", "editor-documents"}, Run: b.server.Server.Sources.Mutations.RecoverFileOperations,
	}); err != nil {
		return err
	}
	owner := b.server.Server.Admin.Extensions.Mutations.Owner
	if err := b.registerRecovery(bootrecovery.Entry{
		Name: "extension-operations", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
		Run: func(ctx context.Context) error {
			if err := extpacks.RecoverMetaPackTransactions(); err != nil {
				return err
			}
			return extensionJournal.Recover(ctx, owner.Publisher, owner.Events)
		},
	}); err != nil {
		return err
	}
	if err := b.registerRecovery(bootrecovery.Entry{
		Name: "workflow-topologies", Kind: bootrecovery.KindReconcile, Phase: bootrecovery.PhaseServe,
		After: []string{"workflow-child-terminals"}, Run: b.server.Server.Admin.Workflow.Topology.RecoverOrchestratedTopologies,
	}); err != nil {
		return err
	}
	if err := b.server.Server.Admin.Extensions.Contributions.WarmContributionFrame(b.startup.ctx); err != nil {
		b.startup.logger.Warn("contribution frame warm failed", "err", err)
	}
	return nil
}

// wireFileBriefings applies the briefing retention policy and builds the service.
func wireFileBriefings(b *serveBuilder, deps *api.Dependencies) error {
	fileBriefingConfig, err := filebriefing.LoadConfig()
	if err != nil {
		return fmt.Errorf("file briefing config: %w", err)
	}
	fileBriefingStore := filebriefing.NewSQL(b.storage.Database)
	if b.settings.Service != nil && b.settings.Service.FileSummaries != nil && !b.settings.Service.FileSummaries.Enabled() {
		if err := fileBriefingStore.Clear(b.startup.ctx); err != nil {
			return fmt.Errorf("clear disabled file briefings: %w", err)
		}
	} else if err := fileBriefingStore.MaintainDevice(b.startup.ctx, fileBriefingConfig.Retention); err != nil {
		return fmt.Errorf("maintain file briefings: %w", err)
	}
	deps.Source.FileBriefings = filebriefing.NewService(b.startup.ctx, filebriefing.Dependencies{
		Store: fileBriefingStore, Config: fileBriefingConfig, Generator: filebriefing.NewModelGenerator(b.providers.Service, b.sessions.Manager.Coordinator.Model.Cost),
		Events: b.events.Hub, Settings: b.settings.Service.FileSummaries, Logger: b.startup.logger,
	})
	return nil
}

// wireSourceEditing builds the source write path shared by the API and the
// session runtime: source mutations, durable file requests, editor documents,
// and the contribution runtime that reads document revisions.
func wireSourceEditing(b *serveBuilder, deps *api.Dependencies) error {
	if b.storage.SourceLedger != nil {
		deps.Source.SourceLedger, deps.Source.SourceInventory = b.storage.SourceLedger, b.storage.SourceLedger.Inventory
	}
	sourceMutations := projectsource.NewSourceMutationService(b.storage.Database, b.storage.SourceLedger)
	if err := b.registerRecovery(bootrecovery.Entry{
		Name: "source-mutations", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
		Run: sourceMutations.Recover,
	}); err != nil {
		return err
	}
	deps.Source.SourceMutations = sourceMutations
	deps.Source.FileOperations = fileops.NewService(fileops.NewStore(b.storage.Database))
	b.sessions.Manager.ToolContext.SetSourceMutations(sourceMutations)
	editorDocuments := editordoc.New(editordoc.NewStore(b.storage.Database), b.storage.SourceLedger, b.storage.SourceLedger.History, b.storage.Projects)
	if b.worker.merge != nil {
		b.worker.merge.Documents = editorDocuments
	}
	b.startup.resources.Track("editor-documents", 86, editorDocuments.Close)
	if err := b.registerRecovery(bootrecovery.Entry{
		Name: "editor-documents", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
		// Editor recovery follows settled source mutations.
		After: []string{"source-mutations"},
		Run: func(ctx context.Context) error {
			return editorDocuments.Recover(ctx)
		},
	}); err != nil {
		return err
	}
	b.events.Agents.SetAnchors(presenceAnchors{service: editorDocuments})
	b.events.Agents.SetDrafts(presenceDrafts{projects: b.storage.Projects, documents: editorDocuments,
		tasks: func(jobID string) (*wire.WorkerTask, bool) {
			if b.delegations == nil || b.delegations.Queue == nil {
				return nil, false
			}
			return b.delegations.Queue.Get(jobID)
		}})
	if err := b.registerRecovery(bootrecovery.Entry{
		// Presence starts empty; drafts awaiting promotion outlive it.
		Name: "agent-presence-worker-drafts", Kind: bootrecovery.KindReconcile, Phase: bootrecovery.PhaseServe,
		After: []string{"worker-merge-applies"},
		Run: func(ctx context.Context) error {
			return restoreWorkerDrafts(b, ctx)
		},
	}); err != nil {
		return err
	}
	deps.Source.EditorDocuments = editorDocuments
	// Disconnected windows leave presence after a bounded reconnect grace.
	deps.Source.EditorClients = editordoc.NewClientLiveness(editordoc.PresenceGrace, func(clientID string) {
		editorDocuments.DisconnectClient(context.Background(), clientID)
	})
	// A file the person has open is the document, for reads and writes alike.
	b.sessions.Manager.ToolContext.SetEditorDocuments(editorDocumentsAdapter{service: editorDocuments})
	b.sessions.Manager.Chats.Rewinds.SetSourceRewinds(&sourcerewind.Service{Planner: b.storage.SourceLedger.Comparisons, Mutations: sourceMutations, Documents: editorDocuments})
	// Contribution dispatch uses durable receipts and policy-derived authority.
	deps.Extensions.Contributions = extensionadmin.ContributionRuntime{
		Receipts: commandinvoke.SQLReceipts{DB: b.storage.Database},
		Authority: commandinvoke.PolicyAuthority{
			FindingNamesPath: func(ctx context.Context, sessionID, path string) (bool, error) {
				if b.boards == nil || b.boards.Findings == nil {
					return false, nil
				}
				stored, err := b.boards.Findings.List(ctx, sessionID, 0)
				if err != nil {
					return false, err
				}
				for _, finding := range stored {
					if findings.RefNamesPath(finding.Ref, path) {
						return true, nil
					}
				}
				return false, nil
			},
			DocumentRevision: func(ctx context.Context, projectID, rootID, path string) (int64, bool, error) {
				p, err := b.storage.Projects.Get(ctx, projectID)
				if err != nil || p == nil {
					return 0, false, err
				}
				return editorDocuments.CurrentRevision(ctx, p, rootID, path)
			},
		},
	}
	return nil
}

func (b *serveBuilder) wireOrchestrator() error {
	b.delegations.Manager.ToolBudget = b.sessions.WorkerToolBudgetFor
	orchDeps := orchestration.OrchestratorDeps{
		Delegation: b.delegations.Manager,
		Store:      b.delegations.Store,
		Agents:     b.agents.Registry,
		Workflows:  &orchestration.WorkflowRunLifecycle{Runs: b.workflows.Store.Runs, Starts: b.workflows.Manager.Starts, Controls: b.workflows.Manager.Controls, Policy: b.workflows.Manager.Policy, Topology: b.workflows.Manager.Phases},
		Catalog:    extpacks.CatalogForConsumers,
	}
	if b.startup.cfg.TestOrchestrator != nil {
		b.server.Orchestrator = b.startup.cfg.TestOrchestrator(orchDeps)
	} else {
		b.server.Orchestrator = orchestration.NewOrchestratorImpl(orchDeps)
	}
	b.workflows.Manager.Presentation.TopologyLegs = orchestration.TopologyLegView{Store: b.delegations.Store, Catalog: extpacks.CatalogForConsumers}

	workerOutcomes := &worker.SessionOutcomeBridge{Workers: b.sessions.Manager.Coordinator.Workers, Loop: b.sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Nudges, Results: b.sessions.Manager.Workers.Results, State: b.sessions.Manager.Workers.State, Closure: b.sessions.Manager.Coordinator.ProgressClosure, Inner: b.delegations.Manager}
	var executor worker.WorkerExecutor = b.delegations.Executor
	if configdir.IsHarnessChannel() {
		scripted, err := harnessfixture.NewWorkers(b.storage.Directory, b.storage.Sessions, b.delegations.Queue, b.sessions.Manager.Workers.Harness.Verify, b.sessions.Manager.Workers.Harness.Read, b.sessions.Decisions, b.delegations.Executor)
		if err != nil {
			return err
		}
		b.worker.harnessWorkers = scripted
		executor = scripted
	}
	b.worker.poller = worker.NewLocalWorkerPoller(b.delegations.Queue, executor, b.worker.cfg, workerOutcomes)
	b.delegations.Queue.SetRunningCancel(b.worker.poller.Abort)
	b.worker.poller.ParentWaiter = b.boards.ParentWaiter
	return nil
}

func (b *serveBuilder) serveApp() *ServeApp {
	app := &ServeApp{
		Server:               b.server.Server,
		Sessions:             b.sessions,
		Workflows:            b.workflows,
		Delegations:          b.delegations,
		Boards:               b.boards,
		ProjectLiveness:      b.server.ProjectLiveness,
		CoordinatorRuntime:   b.server.Coordinator,
		AgentRegistry:        b.agents.Registry,
		ToolRegistry:         b.execution.Host.Registry,
		DB:                   b.storage.Database,
		Events:               b.events.Hub,
		ConfigRoot:           b.catalog.ModuleRoot,
		ListenAddr:           b.startup.addr,
		APIToken:             b.identity.Token,
		TokenGenerated:       b.identity.Generated,
		startup:              b.startup.cfg.Startup,
		upgradeRecoveryReady: b.storage.UpgradeReady,
		resources:            b.startup.resources,
		storeClaim:           b.storage.Claim,
		decider:              b.decisions.Decider,
		projects:             b.storage.Projects,
		eventPub:             b.events.Publisher,
	}
	registerBackgroundRunners(b, app)
	return app
}

// sealApprovalGate activates the fully wired approval gate.
func (b *serveBuilder) sealApprovalGate() error {
	if b.execution.Host == nil {
		return nil
	}
	b.execution.Host.Authority.SealApprovalGate()
	if !b.execution.Host.Authority.ApprovalGateSealed() {
		return fmt.Errorf("approval gate did not seal")
	}
	return nil
}

// wireDestinationConfig registers user-configured destinations and the public
// package registries the egress gate reads.
func wireDestinationConfig(b *serveBuilder) error {
	if b.execution.Host == nil || b.execution.Host.Executor == nil {
		return nil
	}
	registries, err := pkgregistry.Bundled()
	if err != nil {
		return err
	}
	b.execution.Host.Executor.Network.SetPackageRegistries(registries)
	reg := destconfig.NewRegistry()
	if b.providers.Service != nil && b.providers.Service.Registry != nil {
		reg.Add(destconfig.Source{
			Name:  "provider_endpoints",
			Hosts: func(string) []string { return b.providers.Service.Registry.ConfiguredHosts() },
		})
	}
	if b.server.MCP != nil {
		reg.Add(destconfig.Source{
			Name: "mcp_servers",
			Hosts: func(projectDir string) []string {
				return b.server.MCP.Catalog.ConfiguredHosts(b.startup.ctx, projectDir)
			},
		})
	}
	reg.Add(destconfig.Source{
		HostSources: func(projectDir string) map[string]string {
			roots := []string{projectDir}
			if b.storage.Projects != nil {
				if list, err := b.storage.Projects.List(b.startup.ctx); err == nil {
					if p := project.FindByRootPath(list, projectDir); p != nil {
						for _, r := range p.Roots {
							if r.Path != "" {
								roots = append(roots, r.Path)
							}
						}
					}
				}
			}
			return destconfig.GitRemoteHostsForRoots(roots...)
		},
	})
	b.execution.Host.Executor.Network.SetDestinationConfig(reg)
	return nil
}

func restoreWorkerDrafts(b *serveBuilder, ctx context.Context) error {
	if b.delegations == nil || b.delegations.Queue == nil {
		return nil
	}
	pending, err := b.delegations.Queue.ListPendingOverlays(ctx)
	if err != nil {
		return err
	}
	jobs := make([]agentpresence.ReadyJob, 0, len(pending))
	for _, job := range pending {
		if job.ParentSessionID != "" {
			jobs = append(jobs, agentpresence.ReadyJob{ChatSessionID: job.ParentSessionID, JobID: job.JobID})
		}
	}
	b.events.Agents.RestoreReadyDrafts(ctx, jobs)
	return nil
}

type appProjectLifecycleHandler struct {
	activate func(ctx context.Context, projectID string) error
	park     func(ctx context.Context, projectID string) error
}

func (h appProjectLifecycleHandler) OnProjectActivate(ctx context.Context, projectID string) error {
	if h.activate != nil {
		return h.activate(ctx, projectID)
	}
	return nil
}

func (h appProjectLifecycleHandler) OnProjectPark(ctx context.Context, projectID string) error {
	if h.park != nil {
		return h.park(ctx, projectID)
	}
	return nil
}
