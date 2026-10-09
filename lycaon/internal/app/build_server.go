package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/capabilityadmin"
	"github.com/lycaon/lycaon/internal/api/extensionadmin"
	"github.com/lycaon/lycaon/internal/attention"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/browser/preview"
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
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/historyretention"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/people/personactions"
	"github.com/lycaon/lycaon/internal/pkgregistry"
	"github.com/lycaon/lycaon/internal/preflight"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectliveness"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourcerewind"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/heldtools"
	"github.com/lycaon/lycaon/internal/usernotice"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workernotice"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// serverWiring wires the HTTP server, runtime services, preflight, and store upgrade recovery.
type serverWiring struct{ *serveBuilder }

// wireRuntimeServices builds background processes, page sessions, and live
// previews, registers their tools, and installs process-wide observers.
func (b serverWiring) wireRuntimeServices() error {
	b.eventPub.Board = b.boardSnap
	b.bgRegistry = bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{
		Publish: func(ctx context.Context, projectID, sessionID string, ev wire.BackgroundProcessEvent) {
			b.eventPub.PublishProcess(ctx, projectID, sessionID, ev)
		},
		Complete: b.mgr.HandleCommandCompletion,
		Refused:  b.mgr.HandleCommandRefusal,
	})
	b.heldCalls = heldcall.New(func(ctx context.Context, projectID, sessionID string, ev wire.BackgroundProcessEvent) {
		b.eventPub.PublishProcess(ctx, projectID, sessionID, ev)
	}, b.mgr.HandleHeldCallSettled)
	b.pageRegistry = pagesession.NewRegistry(pagesession.DefaultConfig())
	b.previewCtrl = preview.NewController(preview.DefaultConfig(), func(ctx context.Context, projectID, sessionID string, ev wire.PreviewEvent) {
		b.eventPub.PublishPreview(ctx, projectID, sessionID, ev)
	})
	if err := b.mgr.RegisterSessionCleanup("preview-streams", 40, b.previewCtrl.DisposeSession); err != nil {
		return fmt.Errorf("register preview cleanup: %w", err)
	}
	b.pageRegistry.SetOnClose(func(sessionID, pageID string) {
		b.previewCtrl.Detach(context.Background(), sessionID, pageID)
	})
	b.toolRuntime.Commands.SetBackgroundRegistry(b.bgRegistry)
	b.mgr.SetBackgroundRegistry(b.bgRegistry)
	b.mgr.SetHeldCalls(b.heldCalls)
	if err := heldtools.Register(b.toolRuntime.Registry, b.heldCalls); err != nil {
		return fmt.Errorf("held call tools: %w", err)
	}
	b.mgr.SetPageRegistry(b.pageRegistry)
	if err := native.RegisterTerminalSessionTools(b.toolRuntime.Registry, b.bgRegistry); err != nil {
		return fmt.Errorf("terminal session tools: %w", err)
	}
	if b.browserPool != nil {
		if err := native.RegisterCapturePageTool(b.toolRuntime.Registry, b.browserPool, b.bgRegistry, b.previewCtrl); err != nil {
			return fmt.Errorf("capture_page tool: %w", err)
		}
		if err := native.RegisterMeasurePageTool(b.toolRuntime.Registry, b.browserPool, b.pageRegistry, b.bgRegistry); err != nil {
			return fmt.Errorf("measure_page tool: %w", err)
		}
		if err := native.RegisterPageSessionTools(b.toolRuntime.Registry, b.browserPool, b.pageRegistry, b.bgRegistry, b.previewCtrl); err != nil {
			return fmt.Errorf("page session tools: %w", err)
		}
	}
	return b.wireRuntimeObservers()
}

func (b serverWiring) wireRuntimeObservers() error {
	b.mgr.SetEventPublisher(b.eventPub)
	b.workerQueue.SetEventPublisher(b.eventPub)
	if b.llmSvc != nil && b.eventPub != nil {
		b.llmSvc.Lifecycle = utilityLanePublisher{pub: b.eventPub}
	}
	if b.llmSvc != nil && b.llmSvc.Utility != nil && b.eventPub != nil {
		b.llmSvc.Utility.SetOnChange(func(llm.SlotSnapshot) {
			b.eventPub.PublishPreflight(context.Background(), preflight.ProbeLiteSlot)
		})
	}
	findings.RegisterAppendObserver(func(ctx context.Context, evt findings.AppendEvent) {
		if strings.TrimSpace(evt.SessionID) == "" {
			return
		}
		rev := findings.BumpRevision(evt.SessionID)
		b.eventPub.PublishFindings(ctx, evt.SessionID, rev)
	})
	repochange.RegisterObserver(func(ctx context.Context, ev repochange.Event) {
		if b.repoProvider != nil {
			b.repoProvider.Changed(ctx, ev.ProjectDir)
		}
		if ev.Kind == repochange.HeadMoved {
			b.toolRuntime.Survey.InvalidateFileAge(ev.ProjectDir)
		}
	})
	activeRun := activeRunIDFromWorkflow(b.workflowMgr)
	progressCoalescer := progress.NewCoalescer(progress.DefaultCoalesceWindow, newProgressChangeEmitter(b.store, b.eventPub, activeRun, b.progressStore))
	progress.RegisterWriteObserver(func(ctx context.Context, evt progress.WriteEvent) {
		if strings.TrimSpace(evt.SessionID) == "" {
			return
		}
		rev := progress.BumpRevision(evt.SessionID)
		b.eventPub.PublishProgress(ctx, evt.SessionID, rev)
		progressCoalescer.Record(evt.SessionID, evt.Prev, b.progressStore.Get(ctx, evt.SessionID))
		emitProgressCompletion(ctx, b.store, b.eventPub, b.progressStore, activeRun, evt.SessionID)
		// Progress closure releases the post-worker latch.
		if b.mgr != nil {
			b.mgr.MaybeClearProgressClosureAfterWrite(ctx, evt.SessionID)
		}
	})
	return nil
}

// wireServer builds the API server once over the host's services, then
// registers the recoveries and hooks that call into its handlers.
func (b serverWiring) wireServer() error {
	extensionJournal := extensionstate.NewSQLJournal(b.db)
	deps := api.Dependencies{Core: api.CoreDependencies{
		Database: b.db, Store: b.store, PersonActions: personactions.New(b.db), Projects: b.registry, Sessions: b.mgr, Settings: b.settingsSvc,
		Invocations: b.invocations, MutationGate: project.NewMutationGate()}, Providers: api.ProvidersDependencies{LLM: b.llmSvc, CostTracker: b.costTracker, Rerank: b.rerank}, Host: api.HostDependencies{
		Events: b.hub, EventPublisher: b.eventPub, Presence: b.presence, HostIdentity: b.hostIdentity,
		HostResources: b.hostResources, HostPower: b.hostPower, Pricing: b.pricingHost, Preview: b.previewCtrl,
		PreflightEnv: b.buildPreflightEnv()}, Storage: api.StorageDependencies{
		DataDir: b.dataDir, StorePath: b.storePath, WorkerBranchRoot: b.workerBranchRoot, WorkerSeedRoot: b.workerSeedRoot,
		ModuleRoot: b.configRoot, StoreRevision: b.storeRevision, MinDenVersion: os.Getenv("LYCAON_MIN_DEN_VERSION")}, Approvals: api.ApprovalsDependencies{
		SecretIgnores: b.secretIgnores, ManagedSecrets: b.secretCaps, SecretSpans: b.secretSpans,
		Checkpoints: b.checkpointMgr, ApprovalGate: b.toolRuntime.Authority.ApprovalGate(),
		Authority: capabilityadmin.Authority{
			DirectIP: b.directIPCapabilityRT, GrantedPaths: b.grantedPathRT, Listen: b.sandboxListenRT,
			Loopback: b.sandboxLoopbackRT, ReadPaths: b.sandboxReadPathRT, WriteRoots: b.sandboxWriteRootRT,
			Sockets: b.socketCapabilityRT, ChatGrants: chatGrantLedger(b.checkpointMgr), Vault: b.vaultUnlocks,
		}}, Scans: api.ScansDependencies{
		ScanCoordinator: b.scanCoordinator, ScannerRegistry: b.scannerReg, ScanCadence: b.scanCadence,
		GateRepeatLedger: b.gateRepeatRT, PublishDetections: b.detections.publish}, Workflow: api.WorkflowDependencies{
		Workflows: b.workflowMgr, WorkflowCatalog: b.manifestResolver,
		WorkflowRuns: b.workflowStore, WorkflowComposer: b.workflowComposer, WorkflowPersister: b.workflowPersister,
		Blueprints: b.blueprintMgr, Orchestrator: b.orch, Delegations: b.delegationMgr, Workers: b.workerQueue,
		WorkerCancel: b.workerCancelSvc, Board: b.boardSnap, RepoSetCache: b.gitRepoSetCache}, Source: api.SourceDependencies{
		AgentPresence: b.agentPresence, ProjectRules: b.projectRulesOverlay, HintConfig: b.hintCfg,
		FileAgeWarmer: b.toolRuntime.Survey.WarmFileAge, VisualStore: b.visualStore, ProgressStore: b.progressStore,
		Video: toolWiring(b).videoDecoder()}, Extensions: api.ExtensionsDependencies{
		ExtensionViews: b.viewCache, ExtensionJournal: extensionJournal}, External: api.ExternalDependencies{
		MCP: b.mcpReg, WebResearch: b.webResearchRuntime, WebDiscoverer: b.webDiscoverer, WebIndex: b.webIndex}, Harness: api.HarnessDependencies{ManualLLM: b.manualLLM, HarnessWorkers: b.harnessWorkers}}
	if b.authzCapturer != nil {
		deps.Approvals.Authority.AuthzRecorder = b.authzCapturer.Recorder
		deps.Approvals.Authority.ApprovalDecisions = b.authzCapturer.Store
	}
	b.projectLiveness = projectliveness.New(projectliveness.Config{
		Sessions: b.store,
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
	b.resources.track("project-liveness", 85, func(context.Context) error {
		b.projectLiveness.Close()
		return nil
	})
	deps.Source.ProjectLiveness = b.projectLiveness
	b.mgr.SetProjectLiveness(b.projectLiveness)
	b.mgr.SetMutationGate(deps.Core.MutationGate)
	prev, ok, err := db.ReadBootPreviousAppVersion(b.ctx, b.db)
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
	deps.Extensions.ExtensionScanners = scan.RequirementChecker{ModuleRoot: b.configRoot, HomeDir: homeDir}
	b.historyStorage = historyretention.New(b.db, b.storePath, b.visualStore)
	deps.External.HistoryStorage = b.historyStorage
	// HTTP and SSE share one attention source.
	deps.Host.Attention = &attention.Source{
		Sessions:    b.store,
		Checkpoints: b.checkpointMgr,
		Asks:        b.workflowMgr,
		Finishes:    b.store,
		Projects:    attention.RegistryNamer{Registry: b.registry},
	}
	b.eventPub.Attention = deps.Host.Attention
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
	b.srv = api.NewServer(deps, b.logger, b.apiToken)
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
	if err := b.srv.Admin.Extensions.Contributions.WarmContributionFrame(b.ctx); err != nil {
		b.logger.Warn("contribution frame warm failed", "err", err)
	}
	return nil
}

// wireFileBriefings applies the briefing retention policy and builds the service.
func (b serverWiring) wireFileBriefings(deps *api.Dependencies) error {
	fileBriefingConfig, err := filebriefing.LoadConfig()
	if err != nil {
		return fmt.Errorf("file briefing config: %w", err)
	}
	fileBriefingStore := filebriefing.NewSQL(b.db)
	if b.settingsSvc != nil && b.settingsSvc.FileSummaries != nil && !b.settingsSvc.FileSummaries.Enabled() {
		if err := fileBriefingStore.Clear(b.ctx); err != nil {
			return fmt.Errorf("clear disabled file briefings: %w", err)
		}
	} else if err := fileBriefingStore.MaintainDevice(b.ctx, fileBriefingConfig.Retention); err != nil {
		return fmt.Errorf("maintain file briefings: %w", err)
	}
	deps.Source.FileBriefings = filebriefing.NewService(b.ctx, filebriefing.Dependencies{
		Store: fileBriefingStore, Config: fileBriefingConfig, Generator: filebriefing.NewModelGenerator(b.llmSvc, b.mgr.CostTracker()),
		Events: b.hub, Settings: b.settingsSvc.FileSummaries, Logger: b.logger,
	})
	return nil
}

// wireSourceEditing builds the source write path shared by the API and the
// session runtime: source mutations, durable file requests, editor documents,
// and the contribution runtime that reads document revisions.
func (b serverWiring) wireSourceEditing(deps *api.Dependencies) error {
	if b.sourceLedger != nil {
		deps.Source.SourceLedger, deps.Source.SourceInventory = b.sourceLedger, b.sourceLedger
	}
	sourceMutations := project.NewSourceMutationService(b.db, b.sourceLedger)
	if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
		Name: "source-mutations", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
		Run: sourceMutations.Recover,
	}); err != nil {
		return err
	}
	deps.Source.SourceMutations = sourceMutations
	deps.Source.FileOperations = fileops.NewService(fileops.NewStore(b.db))
	b.mgr.SetSourceMutations(sourceMutations)
	editorDocuments := editordoc.New(editordoc.NewStore(b.db), b.sourceLedger, b.registry)
	if b.workerMergeSvc != nil {
		b.workerMergeSvc.Documents = editorDocuments
	}
	b.resources.track("editor-documents", 86, editorDocuments.Close)
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
	b.agentPresence.SetAnchors(presenceAnchors{service: editorDocuments})
	b.agentPresence.SetDrafts(presenceDrafts{projects: b.registry, documents: editorDocuments,
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
	b.mgr.SetSourceRewinds(&sourcerewind.Service{Ledger: b.sourceLedger, Mutations: sourceMutations, Documents: editorDocuments})
	// Contribution dispatch uses durable receipts and policy-derived authority.
	deps.Extensions.Contributions = extensionadmin.ContributionRuntime{
		Receipts: commandinvoke.SQLReceipts{DB: b.db},
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
				p, err := b.registry.Get(ctx, projectID)
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
		Agents:     b.agentRegistry,
		Workflows:  b.workflowMgr,
		Catalog:    extpacks.CatalogForConsumers,
	}
	if b.cfg.TestOrchestrator != nil {
		b.orch = b.cfg.TestOrchestrator(orchDeps)
	} else {
		b.orch = orchestration.NewOrchestratorImpl(orchDeps)
	}
	b.workflowMgr.TopologyLegs = orchestration.TopologyLegView{Store: b.delegationStore, Catalog: extpacks.CatalogForConsumers}

	workerOutcomes := &worker.SessionOutcomeBridge{Sessions: b.mgr, Inner: b.delegationMgr}
	var executor worker.WorkerExecutor = b.workerExec
	if configdir.IsHarnessChannel() {
		scripted, err := harnessfixture.NewWorkers(b.dataDir, b.store, b.workerQueue, b.mgr.VerifyHarnessWorker, b.mgr.ReadHarnessWorker, b.decisionStore, b.workerExec)
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
		SessionStore:         b.store,
		decider:              b.decider,
		ProjectLiveness:      b.projectLiveness,
		CoordinatorRuntime:   b.coordRuntime,
		WorkflowMgr:          b.workflowMgr,
		BlueprintMgr:         b.blueprintMgr,
		DelegationMgr:        b.delegationMgr,
		DelegationStore:      b.delegationStore,
		AgentRegistry:        b.agentRegistry,
		WorkerQueue:          b.workerQueue,
		ToolRegistry:         b.toolRuntime.Registry,
		SessionWorkflowStore: b.sessionWorkflowStore,
		CheckpointMgr:        b.checkpointMgr,
		VisualStore:          b.visualStore,
		DB:                   b.db,
		Events:               b.hub,
		ConfigRoot:           b.configRoot,
		ListenAddr:           b.addr,
		APIToken:             b.apiToken,
		TokenGenerated:       b.tokenGenerated,
		startup:              b.cfg.Startup,
		upgradeRecoveryReady: b.upgradeRecoveryReady,
		resources:            b.resources,
		storeClaim:           b.storeClaim,
		projects:             b.registry,
		eventPub:             b.eventPub,
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
	if b.llmSvc != nil && b.llmSvc.Registry != nil {
		reg.Add(destconfig.Source{
			Name:  "provider_endpoints",
			Hosts: func(string) []string { return b.llmSvc.Registry.ConfiguredHosts() },
		})
	}
	if b.mcpReg != nil {
		reg.Add(destconfig.Source{
			Name:  "mcp_servers",
			Hosts: func(projectDir string) []string { return b.mcpReg.Catalog.ConfiguredHosts(b.ctx, projectDir) },
		})
	}
	reg.Add(destconfig.Source{
		HostSources: func(projectDir string) map[string]string {
			roots := []string{projectDir}
			if b.registry != nil {
				if list, err := b.registry.List(b.ctx); err == nil {
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
	b.agentPresence.RestoreReadyDrafts(ctx, jobs)
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
