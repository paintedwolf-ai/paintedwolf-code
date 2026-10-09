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

// serverWiring wires the HTTP server, runtime services, preflight, and store upgrade recovery.
type serverWiring struct{ *serveBuilder }

func (b serverWiring) wireRuntimeServices() error {
	b.events.Publisher.Board = b.boardSnap
	b.interactions = *interactions.New(b.events.Publisher, b.startup.resources)
	if err := b.interactions.Build(b.mgr.HandleCommandCompletion, b.mgr.HandleCommandRefusal, b.mgr.HandleHeldCallSettled, b.mgr.RegisterSessionCleanup); err != nil {
		return err
	}
	b.toolRuntime.Commands.SetBackgroundRegistry(b.interactions.Processes)
	b.mgr.SetBackgroundRegistry(b.interactions.Processes)
	b.mgr.SetHeldCalls(b.interactions.Calls)
	b.mgr.SetPageRegistry(b.interactions.Pages)
	if err := b.interactions.RegisterTools(b.toolRuntime.Registry, b.browserPool); err != nil {
		return err
	}
	return b.wireRuntimeObservers()
}

func (b serverWiring) wireRuntimeObservers() error {
	b.mgr.SetEventPublisher(b.events.Publisher)
	b.workerQueue.SetEventPublisher(b.events.Publisher)
	observations.Bind(b.events.Publisher, b.storage.Sessions, b.progressStore, b.workflowMgr.Store.Runs, b.mgr.MaybeClearProgressClosureAfterWrite, b.repoProvider, b.toolRuntime.Survey.InvalidateFileAge, b.providers.Service)
	return nil
}

// wireServer builds the API server once over the host's services, then
// registers the recoveries and hooks that call into its handlers.
func (b serverWiring) wireServer() error {
	extensionJournal := extensionstate.NewSQLJournal(b.storage.Database)
	deps := api.Dependencies{Core: api.CoreDependencies{
		Database: b.storage.Database, Store: b.storage.Sessions, PersonActions: personactions.New(b.storage.Database), Projects: b.storage.Projects, Sessions: b.mgr, Settings: b.settings.Service,
		Invocations: b.invocations, MutationGate: project.NewMutationGate()}, Providers: api.ProvidersDependencies{LLM: b.providers.Service, CostTracker: b.providers.Costs, Rerank: b.decisions.Rerank}, Host: api.HostDependencies{
		Events: b.events.Hub, EventPublisher: b.events.Publisher, Presence: b.events.Presence, HostIdentity: b.identity.Host,
		HostResources: b.settings.HostResources, HostPower: b.settings.Power, Pricing: b.providers.Pricing, Preview: b.interactions.Preview,
		PreflightEnv: readiness.New(b.providers.Service, b.decisions.Decider, b.processes.Path).Environment()}, Storage: api.StorageDependencies{
		DataDir: b.storage.Directory, StorePath: b.storage.Path, WorkerBranchRoot: b.workerBranchRoot, WorkerSeedRoot: b.workerSeedRoot,
		ModuleRoot: b.catalog.ModuleRoot, StoreRevision: b.storage.Revision, MinDenVersion: os.Getenv("LYCAON_MIN_DEN_VERSION")}, Approvals: api.ApprovalsDependencies{
		SecretIgnores: b.security.Ignores, ManagedSecrets: b.security.Capabilities, SecretSpans: b.security.Spans,
		Checkpoints: b.checkpointMgr, ApprovalGate: b.toolRuntime.Authority.ApprovalGate(),
		Authority: capabilityadmin.Authority{
			DirectIP: b.security.DirectIP, GrantedPaths: b.grantedPathRT, Listen: b.sandboxListenRT,
			Loopback: b.sandboxLoopbackRT, ReadPaths: b.sandboxReadPathRT, WriteRoots: b.sandboxWriteRootRT,
			Sockets: b.security.Sockets, ChatGrants: chatGrantLedger(b.checkpointMgr), Vault: b.security.Unlocks,
		}}, Scans: api.ScansDependencies{
		ScanCoordinator: b.scanCoordinator, ScannerRegistry: b.scannerReg, ScanCadence: b.scanCadence,
		GateRepeatLedger: b.gateRepeatRT, PublishDetections: b.security.Detections.Publish}, Workflow: api.WorkflowDependencies{
		Workflows: b.workflowMgr, WorkflowCatalog: b.manifestResolver,
		WorkflowRuns: b.workflowStore, WorkflowComposer: b.workflowComposer, WorkflowPersister: b.workflowPersister,
		Blueprints: b.blueprintMgr, Orchestrator: b.orch, Delegations: b.delegationMgr, Workers: b.workerQueue,
		WorkerCancel: b.workerCancelSvc, Board: b.boardSnap, RepoSetCache: b.gitRepoSetCache}, Source: api.SourceDependencies{
		AgentPresence: b.events.Agents, ProjectRules: b.projectRulesOverlay, HintConfig: b.hintCfg,
		FileAgeWarmer: b.toolRuntime.Survey.WarmFileAge, VisualStore: b.visualStore, ProgressStore: b.progressStore,
		Video: toolWiring(b).videoDecoder()}, Extensions: api.ExtensionsDependencies{
		ExtensionViews: b.catalog.ViewCache, ExtensionJournal: extensionJournal}, External: api.ExternalDependencies{
		MCP: b.mcpReg, WebResearch: b.webResearchRuntime, WebDiscoverer: b.webDiscoverer, WebIndex: b.storage.WebIndex}, Harness: api.HarnessDependencies{ManualLLM: b.providers.Manual, HarnessWorkers: b.harnessWorkers}}
	if b.security.Authority != nil {
		deps.Approvals.Authority.AuthzRecorder = b.security.Authority.Recorder
		deps.Approvals.Authority.ApprovalDecisions = b.security.Authority.Store
	}
	b.projectLiveness = projectliveness.New(projectliveness.Config{
		Sessions: b.storage.Sessions,
		Handler: appProjectLifecycleHandler{
			activate: func(ctx context.Context, projectID string) error {
				if b.srv != nil {
					b.srv.Sources.Watch.ScheduleSourceWatch(ctx, projectID)
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
		b.projectLiveness.Close()
		return nil
	})
	deps.Source.ProjectLiveness = b.projectLiveness
	b.mgr.SetProjectLiveness(b.projectLiveness)
	b.mgr.SetMutationGate(deps.Core.MutationGate)
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
	b.userNoticeCatalog = usernotice.NewCatalog(userNoticeCfg)
	deps.Core.UserNotices = b.userNoticeCatalog
	b.workerQueue.SetFailureRenderer(workernotice.NewRenderer(b.userNoticeCatalog))
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		return fmt.Errorf("extension subsystem owner: %w", err)
	}
	deps.Extensions.ExtensionScanners = scan.RequirementChecker{ModuleRoot: b.catalog.ModuleRoot, HomeDir: homeDir}
	b.historyStorage = historyretention.New(b.storage.Database, b.storage.Path, b.visualStore)
	deps.External.HistoryStorage = b.historyStorage
	// HTTP and SSE share one attention source.
	deps.Host.Attention = &attention.Source{
		Sessions:    b.storage.Sessions,
		Checkpoints: b.checkpointMgr,
		Asks:        b.workflowMgr.Asks,
		Finishes:    b.storage.Sessions,
		Projects:    attention.RegistryNamer{Registry: b.storage.Projects},
	}
	b.events.Publisher.Attention = deps.Host.Attention
	if manager, ok := b.checkpointMgr.(*hitl.Checkpoints); ok {
		if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
			Name: "approval-operations", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
			Run: manager.Authority.RecoverApprovalOperations,
		}); err != nil {
			return err
		}
		// Chat approvals outlive restart; they replay after unfinished approvals roll back.
		if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
			Name: "chat-grants", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
			After: []string{"approval-operations"},
			Run:   manager.Authority.RestoreChatGrants,
		}); err != nil {
			return err
		}
	}
	if err := b.wireFileBriefings(&deps); err != nil {
		return err
	}
	if err := b.wireSourceEditing(&deps); err != nil {
		return err
	}
	b.srv = api.NewServer(deps, b.startup.logger, b.identity.Token)
	return b.registerServerHooks(extensionJournal)
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
func (b serverWiring) registerServerHooks(extensionJournal *extensionstate.SQLJournal) error {
	// Publish execution failures before queued follow-up work can delay the caller.
	b.mgr.SetTurnFailureSink(b.srv.Admin.Prompt.Execution.PublishTurnFailure)
	b.mgr.SetPromotionHook(b.srv.Admin.Project.Promotion.TryRunPromotion)
	b.mgr.SetProjectSandboxReconcile(b.srv.Admin.Project.Sandboxes.ScheduleProjectSandboxReconcile)
	// Source views addressed by a chat end with it.
	if err := b.mgr.RegisterSessionDisposal("source-views", 60, func(_ context.Context, sessionID string) error {
		b.srv.Sources.Views.ReleaseChatSourceViews(sessionID)
		return nil
	}); err != nil {
		return err
	}
	// Serve recovery begins after runtime gates are sealed.
	if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
		Name: "prompt-submissions", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseServe,
		Run: b.srv.Admin.Prompt.Execution.RecoverPromptSubmissions,
	}); err != nil {
		return err
	}
	if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
		Name: "project-promotions", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
		Run: b.srv.Admin.Project.Promotion.RecoverPromotions,
	}); err != nil {
		return err
	}
	if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
		Name: "source-file-requests", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
		After: []string{"source-mutations", "editor-documents"}, Run: b.srv.Sources.Mutations.RecoverFileOperations,
	}); err != nil {
		return err
	}
	owner := b.srv.Admin.Extensions.Mutations.Owner
	if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
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
	if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
		Name: "workflow-topologies", Kind: bootrecovery.KindReconcile, Phase: bootrecovery.PhaseServe,
		After: []string{"workflow-child-terminals"}, Run: b.srv.Admin.Workflow.Topology.RecoverOrchestratedTopologies,
	}); err != nil {
		return err
	}
	if err := b.srv.Admin.Extensions.Contributions.WarmContributionFrame(b.startup.ctx); err != nil {
		b.startup.logger.Warn("contribution frame warm failed", "err", err)
	}
	return nil
}

// wireFileBriefings applies the briefing retention policy and builds the service.
func (b serverWiring) wireFileBriefings(deps *api.Dependencies) error {
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
		Store: fileBriefingStore, Config: fileBriefingConfig, Generator: filebriefing.NewModelGenerator(b.providers.Service, b.mgr.CostTracker()),
		Events: b.events.Hub, Settings: b.settings.Service.FileSummaries, Logger: b.startup.logger,
	})
	return nil
}

// wireSourceEditing builds the source write path shared by the API and the
// session runtime: source mutations, durable file requests, editor documents,
// and the contribution runtime that reads document revisions.
func (b serverWiring) wireSourceEditing(deps *api.Dependencies) error {
	if b.storage.SourceLedger != nil {
		deps.Source.SourceLedger, deps.Source.SourceInventory = b.storage.SourceLedger, b.storage.SourceLedger
	}
	sourceMutations := project.NewSourceMutationService(b.storage.Database, b.storage.SourceLedger)
	if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
		Name: "source-mutations", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
		Run: sourceMutations.Recover,
	}); err != nil {
		return err
	}
	deps.Source.SourceMutations = sourceMutations
	deps.Source.FileOperations = fileops.NewService(fileops.NewStore(b.storage.Database))
	b.mgr.SetSourceMutations(sourceMutations)
	editorDocuments := editordoc.New(editordoc.NewStore(b.storage.Database), b.storage.SourceLedger, b.storage.Projects)
	if b.workerMergeSvc != nil {
		b.workerMergeSvc.Documents = editorDocuments
	}
	b.startup.resources.Track("editor-documents", 86, editorDocuments.Close)
	if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
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
			if b.workerQueue == nil {
				return nil, false
			}
			return b.workerQueue.Get(jobID)
		}})
	if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
		// Presence starts empty; drafts awaiting promotion outlive it.
		Name: "agent-presence-worker-drafts", Kind: bootrecovery.KindReconcile, Phase: bootrecovery.PhaseServe,
		After: []string{"worker-merge-applies"},
		Run:   b.restoreWorkerDrafts,
	}); err != nil {
		return err
	}
	deps.Source.EditorDocuments = editorDocuments
	// Disconnected windows leave presence after a bounded reconnect grace.
	deps.Source.EditorClients = editordoc.NewClientLiveness(editordoc.PresenceGrace, func(clientID string) {
		editorDocuments.DisconnectClient(context.Background(), clientID)
	})
	// A file the person has open is the document, for reads and writes alike.
	b.mgr.SetEditorDocuments(editorDocumentsAdapter{service: editorDocuments})
	b.mgr.SetSourceRewinds(&sourcerewind.Service{Ledger: b.storage.SourceLedger, Mutations: sourceMutations, Documents: editorDocuments})
	// Contribution dispatch uses durable receipts and policy-derived authority.
	deps.Extensions.Contributions = extensionadmin.ContributionRuntime{
		Receipts: commandinvoke.SQLReceipts{DB: b.storage.Database},
		Authority: commandinvoke.PolicyAuthority{
			FindingNamesPath: func(ctx context.Context, sessionID, path string) (bool, error) {
				if b.findingsStore == nil {
					return false, nil
				}
				stored, err := b.findingsStore.List(ctx, sessionID, 0)
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

func (b serverWiring) wireOrchestrator() error {
	b.delegationMgr.ToolBudget = b.workerToolBudgetFor
	orchDeps := orchestration.OrchestratorDeps{
		Delegation: b.delegationMgr,
		Store:      b.delegationStore,
		Agents:     b.agents.Registry,
		Workflows:  &orchestration.WorkflowRunLifecycle{Runs: b.workflowMgr.Store.Runs, Starts: b.workflowMgr.Starts, Controls: b.workflowMgr.Controls, Policy: b.workflowMgr.Policy, Topology: b.workflowMgr.Phases},
		Catalog:    extpacks.CatalogForConsumers,
	}
	if b.startup.cfg.TestOrchestrator != nil {
		b.orch = b.startup.cfg.TestOrchestrator(orchDeps)
	} else {
		b.orch = orchestration.NewOrchestratorImpl(orchDeps)
	}
	b.workflowMgr.Presentation.TopologyLegs = orchestration.TopologyLegView{Store: b.delegationStore, Catalog: extpacks.CatalogForConsumers}

	workerOutcomes := &worker.SessionOutcomeBridge{Sessions: b.mgr, Inner: b.delegationMgr}
	var executor worker.WorkerExecutor = b.workerExec
	if configdir.IsHarnessChannel() {
		scripted, err := harnessfixture.NewWorkers(b.storage.Directory, b.storage.Sessions, b.workerQueue, b.mgr.VerifyHarnessWorker, b.mgr.ReadHarnessWorker, b.decisionStore, b.workerExec)
		if err != nil {
			return err
		}
		b.harnessWorkers = scripted
		executor = scripted
	}
	b.workerPoller = worker.NewLocalWorkerPoller(b.workerQueue, executor, b.workersCfg, workerOutcomes)
	b.workerQueue.SetRunningCancel(b.workerPoller.Abort)
	b.workerPoller.ParentWaiter = b.parentWorkerWaiter
	return nil
}

func (b serverWiring) serveApp() *ServeApp {
	app := &ServeApp{
		Server:               b.srv,
		SessionMgr:           b.mgr,
		SessionStore:         b.storage.Sessions,
		decider:              b.decisions.Decider,
		ProjectLiveness:      b.projectLiveness,
		CoordinatorRuntime:   b.coordRuntime,
		WorkflowMgr:          b.workflowMgr,
		BlueprintMgr:         b.blueprintMgr,
		DelegationMgr:        b.delegationMgr,
		DelegationStore:      b.delegationStore,
		AgentRegistry:        b.agents.Registry,
		WorkerQueue:          b.workerQueue,
		ToolRegistry:         b.toolRuntime.Registry,
		SessionWorkflowStore: b.sessionWorkflowStore,
		CheckpointMgr:        b.checkpointMgr,
		VisualStore:          b.visualStore,
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
		projects:             b.storage.Projects,
		eventPub:             b.events.Publisher,
	}
	delegationWiring(b).registerBackgroundRunners(app)
	return app
}

// sealApprovalGate activates the fully wired approval gate.
func (b serverWiring) sealApprovalGate() error {
	if b.toolRuntime == nil {
		return nil
	}
	b.toolRuntime.Authority.SealApprovalGate()
	if !b.toolRuntime.Authority.ApprovalGateSealed() {
		return fmt.Errorf("approval gate did not seal")
	}
	return nil
}

// wireDestinationConfig registers user-configured destinations and the public
// package registries the egress gate reads.
func (b serverWiring) wireDestinationConfig() error {
	if b.toolRuntime == nil || b.toolRuntime.Executor == nil {
		return nil
	}
	registries, err := pkgregistry.Bundled()
	if err != nil {
		return err
	}
	b.toolRuntime.Executor.Network.SetPackageRegistries(registries)
	reg := destconfig.NewRegistry()
	if b.providers.Service != nil && b.providers.Service.Registry != nil {
		reg.Add(destconfig.Source{
			Name:  "provider_endpoints",
			Hosts: func(string) []string { return b.providers.Service.Registry.ConfiguredHosts() },
		})
	}
	if b.mcpReg != nil {
		reg.Add(destconfig.Source{
			Name:  "mcp_servers",
			Hosts: func(projectDir string) []string { return b.mcpReg.Catalog.ConfiguredHosts(b.startup.ctx, projectDir) },
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
	b.toolRuntime.Executor.Network.SetDestinationConfig(reg)
	return nil
}

func (b serverWiring) restoreWorkerDrafts(ctx context.Context) error {
	if b.workerQueue == nil {
		return nil
	}
	pending, err := b.workerQueue.ListPendingOverlays(ctx)
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
