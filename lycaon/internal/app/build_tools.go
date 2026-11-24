package app

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/app/sessions"
	"github.com/lycaon/lycaon/internal/app/workerruntime"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/boot"
	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/parse"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretspan"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/watch"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workerlane"
	"github.com/lycaon/lycaon/internal/workerops"
	workflowstatetools "github.com/lycaon/lycaon/internal/workflow/statetools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (b *serveBuilder) wireCoordinatorRuntime() error {
	b.server.Coordinator = b.sessions.Manager.Coordinator.Runtime
	waitConditions := make(map[string]map[string]bool, len(b.agents.ToolProfiles))
	watchConditions := make(map[string]map[string]bool, len(b.agents.ToolProfiles))
	for _, profile := range b.agents.ToolProfiles {
		allowed := make(map[string]bool, len(profile.WaitConditions))
		for _, condition := range profile.WaitConditions {
			allowed[condition] = true
		}
		waitConditions[profile.ID] = allowed
		allowedWatch := make(map[string]bool, len(profile.WatchConditions))
		for _, condition := range profile.WatchConditions {
			allowedWatch[condition] = true
		}
		watchConditions[profile.ID] = allowedWatch
	}
	waitStore := &awaitstore.Store{DB: b.storage.Database}
	subscriptions := b.server.Coordinator.CoordinatorLoop().Waits.Subscriptions
	watchStore := newCoordinatorWatchStore(b.storage.Database, b.sessions, subscriptions, b.workerRuntime)
	wireCoordinatorWatchDelivery(watchStore, b.storage.Database, b.sessions, b.server.Coordinator.CoordinatorLoop())
	if watchStore != nil {
		if b.execution.Host != nil && b.execution.Host.Commands != nil {
			b.execution.Host.Commands.SetDerivedWatches(derivedCommandWatchAdapter{store: watchStore})
		}
		wireWatchProducers(b, watchStore)
	}
	workerKnownFn := waitWorkerKnown(b.boards.WorkerRecords, b.workerRuntime.Queue)
	if err := loopwake.RegisterWaitTool(b.execution.Host.Registry, subscriptions, loopwake.WaitToolDeps{
		Store: waitStore, Watches: watchStore, ProfileConditions: waitConditions,
		SecretMatcher: b.security.Matcher, RuntimeContext: b.startup.ctx,
		WorkerKnown: workerKnownFn,
	}); err != nil {
		return fmt.Errorf("wait tool: %w", err)
	}
	if err := watch.RegisterWatchTools(b.execution.Host.Registry, watch.ToolDeps{
		Store:             watchStore,
		ProfileConditions: watchConditions,
		SecretMatcher:     b.security.Matcher,
		WorkerKnown: func(sessionID, handle string) bool {
			return workerKnownFn == nil || workerKnownFn(b.startup.ctx, sessionID, handle)
		},
		ProcessKnown: func(sessionID, handle string) bool {
			if b.sessions == nil || b.sessions.Manager == nil || b.sessions.Manager.Processes == nil || b.sessions.Manager.Processes.Background == nil {
				return true
			}
			known, _ := b.sessions.Manager.Processes.Background.State(sessionID, handle)
			return known
		},
		ProjectChat: func(ctx context.Context, projectID string) bool {
			if b.storage.Projects == nil || projectID == "" {
				return false
			}
			p, err := b.storage.Projects.Get(ctx, projectID)
			return err == nil && p != nil && !p.IsDraft
		},
	}); err != nil {
		return fmt.Errorf("watch tools: %w", err)
	}
	if err := b.wireWaitRecovery(subscriptions, waitStore); err != nil {
		return err
	}

	if err := boot.ValidateServeWiring(boot.ServeWiring{
		PostureRegistry: b.agents.Postures,
		BundledRules:    b.workflows.BundledRules,
		RuleEngine:      b.workflows.Rules,
		SessionManager:  b.sessions.Manager,
		WorkflowManager: b.workflows.Manager,
	}); err != nil {
		return fmt.Errorf("serve wiring: %w", err)
	}
	return nil
}

func newCoordinatorWatchStore(
	database *db.Store,
	sessRuntime *sessions.Runtime,
	subscriptions *loopwake.WaitSubscriptions,
	workerRuntime *workerruntime.Runtime,
) *watch.Store {
	if database == nil {
		return nil
	}
	watchStore := watch.NewStore(database, nil)
	if sessRuntime != nil && sessRuntime.Manager != nil && sessRuntime.Manager.Processes != nil {
		processes := sessRuntime.Manager.Processes
		processes.SetWatches(watchStore)
		watchStore.SetProcessLookup(func(_ context.Context, sessionID, handle string) (watch.ProcessCompletionFact, bool, error) {
			fact, ok := processes.CompletedReport(sessionID, handle)
			return fact, ok, nil
		})
	}
	watchStore.SetLeaseWakeHandler(func(ctx context.Context, lease awaitstore.Lease, winner awaitstore.Condition) {
		if lease.WorkerJobID != "" {
			if subscriptions != nil && subscriptions.Waits != nil {
				subscriptions.Waits.BreakSleep(ctx, lease.SessionID, winner.Kind, true)
			}
			if workerRuntime != nil && workerRuntime.Queue != nil {
				_, _ = workerRuntime.Queue.ResumeReadyWaits(ctx)
			}
			return
		}
		if subscriptions != nil && subscriptions.Deliveries != nil {
			subscriptions.Deliveries.NotifyWatchLeaseResolved(ctx, lease.SessionID, lease.ID, winner)
		}
	})
	return watchStore
}

func wireCoordinatorWatchDelivery(
	watchStore *watch.Store,
	database *db.Store,
	sessRuntime *sessions.Runtime,
	coordLoop *loopwake.LoopEngine,
) {
	if watchStore == nil || database == nil || sessRuntime == nil || sessRuntime.Manager == nil {
		return
	}
	mgr := sessRuntime.Manager
	mgr.SetWatchDelivery(&watch.Delivery{
		Store: watchStore,
		WorkerJobID: func(ctx context.Context, childSessionID string) string {
			var jobID string
			_ = database.QueryRowContext(ctx,
				`SELECT id FROM worker_jobs WHERE child_session_id = ? ORDER BY created_at DESC LIMIT 1`,
				childSessionID).Scan(&jobID)
			return jobID
		},
	})
	if coordLoop != nil {
		watchStore.SetAmbientWakeGate(watch.AmbientWakeGate{
			SessionEligible: mgr.Submissions.AmbientWakeEligible,
			WorkflowAdmit:   coordLoop.Admission.AdmitAmbientWatchWake,
			DispatchTurn: func(ctx context.Context, sessionID, submissionID, text string, onAdmitted func() error) error {
				_, err := mgr.Submissions.AmbientWatchWake(ctx, sessionID, submissionID, text, onAdmitted)
				return err
			},
		})
	}
	if mgr.Coordinator.Guidance != nil {
		mgr.Coordinator.Guidance.SetKickAdmittedObserver(func(ctx context.Context, sessionID, kickID, subject, receiptID string) {
			if anchor.SameInform(kickID, anchor.ProcessFinished) && subject != "" {
				_ = watch.MarkPresentationAdmittedByHandle(ctx, watchStore, sessionID, subject, receiptID)
			}
		})
	}
	if mgr.Submissions != nil && mgr.Submissions.Gate != nil {
		gate := mgr.Submissions.Gate
		watchStore.SetTreeStoppingFunc(func(sessionID string) bool {
			return gate.InProgress(context.Background(), sessionID)
		})
	}
	if mgr.Stops != nil {
		mgr.Stops.SetWatchStop(watchTreeStopAdapter{store: watchStore})
	}
	if mgr.Runner != nil && mgr.Runner.Instructions != nil {
		mgr.Runner.Instructions.SetWatchFenceClearer(watchStore.ClearSessionStopFence)
	}
}

func (b *serveBuilder) registerCoordinatorTools() error {
	if err := b.workerRuntime.RegisterPlanTools(b.execution.Host.Registry); err != nil {
		return fmt.Errorf("blueprint worker plan tools: %w", err)
	}
	parseSvc := parse.NewDefaultService()
	if err := parse.RegisterParseTools(b.execution.Host.Registry, parseSvc); err != nil {
		return fmt.Errorf("parse tools: %w", err)
	}
	if err := workflowstatetools.RegisterStateTools(b.execution.Host.Registry, workflowstatetools.StateToolDeps{Runs: b.workflows.Store.Runs, Vars: b.workflows.Manager.Phases.Vars, Journal: b.workflows.Manager.Phases.Journal, Resolver: &b.workflows.Manager.Resolver, Starts: b.workflows.Manager.Starts, Controls: b.workflows.Manager.Controls, Scaffold: b.workflows.Manager.Blueprints.Scaffold,
		Sessions: b.storage.Sessions.Sessions,
	}); err != nil {
		return fmt.Errorf("state tools: %w", err)
	}
	if err := blueprint.RegisterPlanTools(b.execution.Host.Registry, b.workflows.Blueprints); err != nil {
		return fmt.Errorf("plan tools: %w", err)
	}
	waits := &awaitstore.Store{DB: b.storage.Database}
	workerOps := &workerops.Service{
		Reader: b.boards.WorkerReader(), Agents: prompts.AgentMutationCapable,
		Accounted: b.boards.ReleaseAccounted, Release: b.workerRuntime.Queue.ReleaseAdmitted,
		WakeParked: func(ctx context.Context, jobID string) error {
			settled, err := waits.SettleWorkerWait(ctx, jobID, awaitstore.Condition{Kind: "instruction", Outcome: "received"})
			if err != nil || !settled {
				return err
			}
			_, err = b.workerRuntime.Queue.ResumeReadyWaits(ctx)
			return err
		},
	}
	taskDeps := taskToolDeps(b)
	taskDeps.Reviews = workerOps
	b.sessions.Manager.Coordinator.Guards.SetTaskIdentityResolver(taskDeps.IdentityResolver())
	b.sessions.Manager.Coordinator.Guards.SetAskInstructionCheck(workerOps.CheckAskInstruction)
	if err := worker.RegisterTaskTool(b.execution.Host.Registry, taskDeps); err != nil {
		return fmt.Errorf("task tool: %w", err)
	}
	if err := worker.RegisterAnswerDecisionTool(b.execution.Host.Registry, worker.AnswerDecisionToolDeps{
		Answer: b.boards.AnswerDecision, Workers: workerOps,
	}); err != nil {
		return fmt.Errorf("answer_decision tool: %w", err)
	}
	if err := worker.RegisterWorkerTools(b.execution.Host.Registry, worker.WorkerToolDeps{
		Ops: workerOps, Queue: b.workerRuntime.Queue, Workers: b.worker.cfg, Plan: b.workerRuntime.Plan, BindWorkflowTask: b.workflows.Manager.Fanout.BindWorkflowTask, ComposePrompt: b.composeWorkerAssignment,
	}); err != nil {
		return fmt.Errorf("worker tools: %w", err)
	}
	b.execution.Host.Boundary.SetMergeReconcileAllowlister(b.sessions.Manager.Promotion)
	lanes := &worker.WorkerLanes{
		Lanes:   workerlane.New(b.workerRuntime.Workspace, b.boards.WorkerRecords),
		Records: b.boards.WorkerRecords,
		State:   workerlane.NewM14State(b.workerRuntime.Queue),
	}
	if b.workerRuntime.Recovery != nil {
		lanes.Checkpoints = b.workerRuntime.Recovery
	}
	b.workerRuntime.Queue.Branches.SetWorkerLanes(lanes)
	workerMergeSvc := &worker.MergeService{
		Lanes: lanes, Queue: b.workerRuntime.Queue, Branches: b.workerRuntime.Queue.Branches, Store: b.workerRuntime.Promotions, Workspace: b.workerRuntime.Workspace,
		Reject: b.execution.Rejections, Names: b.boards.WorkerRecords, Sessions: b.workerRuntime.Queue,
		Reconcile: b.sessions.Manager.Promotion, Captures: b.sessions.Manager.Chats.Captures,
		Coord:      b.sessions.Manager.Workers.Workspaces,
		OnPromoted: b.workerRuntime.NotifyPromotedTask, Projects: b.storage.Projects, Scans: b.scanning.Triggers,
		SourceLedger: b.storage.SourceLedger, SourceHistory: b.storage.SourceLedger.Walk,
		DataDir: b.storage.Directory, Accounted: b.boards.ReleaseAccounted,
		Reports: worker.ChangeReportDeps{
			SourceRuns: b.sessions.Manager.Verification.WorkerSourceRuns,
			Messages: func(ctx context.Context, childSessionID string) ([]wire.Message, error) {
				return b.storage.Sessions.Transcript.GetMessages(ctx, childSessionID)
			},
		},
	}
	workerMergeSvc.Evidence = worker.SourceEvidenceContext{
		Workspaces:     b.sessions.Manager.Workers.Workspaces,
		SourceRevision: b.sessions.Manager.Verification.WorkerRevision,
		DeclaredCommand: func(ctx context.Context, task *wire.WorkerTask) string {
			if task == nil {
				return ""
			}
			return b.sessions.Manager.Verification.SourceVerifyCommand(ctx, task.WorkspacePath)
		},
	}
	b.worker.merge = workerMergeSvc
	if err := b.registerRecovery(bootrecovery.Entry{
		// Reconcile database and worktree state under the lease after graph wiring.
		Name: "worker-merge-applies", Kind: bootrecovery.KindReconcile, Phase: bootrecovery.PhaseServe,
		Run: workerMergeSvc.RecoverOrphanedMergeApplies,
	}); err != nil {
		return err
	}
	if err := worker.RegisterWorkerCancelTool(b.execution.Host.Registry, worker.CancelToolDeps{
		Cancel:  b.workerRuntime.Cancel,
		Workers: workerOps,
	}); err != nil {
		return fmt.Errorf("worker_cancel tool: %w", err)
	}
	b.sessions.Manager.ProjectControl.SetOverlayPromoter(workerMergeSvc)
	if err := worker.RegisterOverlayTools(b.execution.Host.Registry, worker.OverlayToolDeps{
		Merge:   workerMergeSvc,
		Results: workerOps,
	}); err != nil {
		return fmt.Errorf("overlay tools: %w", err)
	}
	if err := tools.ValidateBootToolClaimsHonest(b.execution.Host.Registry); err != nil {
		return fmt.Errorf("boot tool claims: %w", err)
	}
	profileByID := make(map[string]sandbox.ToolProfile, len(b.agents.ToolProfiles))
	for _, p := range b.agents.ToolProfiles {
		profileByID[p.ID] = p
	}
	if err := orchestration.ValidateAgentSkillSurface(b.agents.Registry, profileByID); err != nil {
		return fmt.Errorf("agent skill surface: %w", err)
	}
	return nil
}

// taskToolDeps wires the task tool to the queue, the workflow's planned legs,
// and the worker assignment prompt.
func taskToolDeps(b *serveBuilder) worker.TaskToolDeps {
	return worker.TaskToolDeps{
		Queue:            b.workerRuntime.Queue,
		Agents:           b.agents.Registry,
		Workers:          b.worker.cfg,
		BindWorkflowTask: b.workflows.Manager.Fanout.BindWorkflowTask,
		WorkflowWork:     b.workflows.Manager.Fanout.WorkflowWork,
		TaskReceipt:      b.workerRuntime.Queue.TaskReceipt,
		WorkerRecords:    b.boards.WorkerRecords,
		Plan:             b.workerRuntime.Plan,
		ComposePrompt:    b.composeWorkerAssignment,
		WebSearchEnabled: func() bool {
			if b.boards == nil || b.boards.WebRuntime.Config == nil {
				return true
			}
			return b.boards.WebRuntime.Config.SearchEnabled()
		},
	}
}

func (b *serveBuilder) wireMCP() error {
	mcpOpts := mcp.RuntimeOptions{
		OnSettingsChange: func() {
			if b.events.Hub != nil {
				_ = b.events.Hub.Publish(b.startup.ctx, wire.EventTopicSettings, events.PublishKey{Facet: string(wire.SettingsAreaMcp)}, wire.SettingsEvent{
					Area:   wire.SettingsAreaMcp,
					Scope:  wire.SettingsScopeGlobal,
					Action: "updated",
				})
			}
		},
	}
	if b.startup.cfg.TestMCPConnector != nil {
		mcpOpts.Connector = b.startup.cfg.TestMCPConnector
	}
	if b.startup.cfg.TestMCPGlobalOverridePath != "" {
		mcpOpts.GlobalOverridePath = b.startup.cfg.TestMCPGlobalOverridePath
	}
	var err error
	b.server.MCP, err = mcp.NewRuntime(mcpOpts)
	if err != nil {
		return fmt.Errorf("mcp registry: %w", err)
	}
	b.startup.resources.setMCP(b.server.MCP)
	b.security.BindMCPInventory(b.server.MCP.Catalog)
	b.server.MCP.Tools.SetToolRegistry(b.execution.Host.Registry)
	b.server.MCP.Connections.SetAPIAccess(b.identity.Token)
	if b.execution.Host != nil {
		b.execution.Host.Authority.SetMCPToolPinSource(b.server.MCP.Tools)
	}
	b.server.MCP.Catalog.SetProjectOverlayGate(b.settings.ProjectSurfaceGate(projectcontrib.SurfaceProjectMCP, b.storage.Projects).AppliesPath)
	if err := wireDestinationConfig(b); err != nil {
		return err
	}
	// Device inspection may inspect every registered project root.
	b.server.MCP.Connections.SetDeviceProbeRoots(func() []string {
		if b.boards == nil {
			return nil
		}
		paths, err := b.boards.ProjectRootPaths(b.startup.ctx)
		if err != nil {
			return nil
		}
		return paths
	})
	if matcher, err := b.security.LoadMatcher(b.startup.ctx, b.startup.cfg.TestSecretMatcher); err != nil {
		return err
	} else {
		var capturePrimer captureprojection.ManagedSecretPrimer
		if b.security.Capabilities != nil {
			capturePrimer = b.security.Capabilities.RememberProjectValues
		}
		captureProjector := captureprojection.New(matcher, capturePrimer)
		if b.security.Capabilities != nil {
			captureProjector.SetManagedSecretGeneration(b.security.Capabilities.ScreeningGeneration)
		}
		if b.interactions.Processes != nil {
			b.interactions.Processes.Output.SetCaptureProjector(captureProjector)
		}
		if recorder, ok := b.sessions.Invocations.(*invocation.SQLRecorder); ok && b.execution.Host != nil && b.execution.Host.Commands != nil {
			programSettings, err := settings.LoadCommandProgramsSettings()
			if err != nil {
				return err
			}
			if err := b.execution.Host.Commands.SetProgramServices(toolhost.ProgramServices{
				Recorder: recorder, Blobs: blobstore.Store{Root: b.storage.Directory}, Projector: captureProjector,
				Progress: b.sessions.Manager.Coordinator.Guards, Sessions: b.storage.Sessions.Sessions,
				Provenance: b.storage.Sessions.Evidence, SourceObserver: b.storage.Sessions.Evidence,
				SourceRuns: b.sessions.Manager.Verification, ExternalHidden: programSettings.ExternalHidden(),
			}); err != nil {
				return err
			}
		}
		if b.interactions.Preview != nil {
			b.interactions.Preview.SetCaptureProjector(captureProjector)
		}
		if b.boards != nil && b.boards.BrowserPool != nil {
			b.boards.BrowserPool.SetCaptureProjector(captureProjector)
		}
		if b.boards != nil && b.boards.BrowserRaster != nil {
			b.boards.BrowserRaster.SetCaptureProjector(captureProjector)
		}
		b.server.MCP.Calls.SetSecretScreen(matcher, b.security.Ask(b.execution.Host.Executor.Secrets, b.execution.Host.Authority.ApprovalsDisabled))
		if b.execution.Host != nil && b.execution.Host.Executor != nil {
			b.execution.Host.Executor.Secrets.SetSecretMatcher(matcher)
			b.execution.Host.Executor.Secrets.SetSecretIgnores(b.security.Ignores)
		}
		// Editor spans preview outbound screening.
		b.security.Spans = secretspan.New(matcher)
		b.security.BindTranscript(matcher, func(redactor func(context.Context, wire.Message) (wire.Message, bool)) {
			b.sessions.Manager.Runner.Transcript.SetRedactor(redactor)
		}, b.sessions.Manager.Runner.Transcript.SweepSessionTree)
		if b.providers.Service != nil && b.providers.Service.Registry != nil {
			screen := llm.NewModelSecretScreen(matcher, b.security.Ask(b.execution.Host.Executor.Secrets, b.execution.Host.Authority.ApprovalsDisabled))
			if b.security.Capabilities != nil {
				screen.SetManagedSecretEvidence(b.security.ModelScreeningValues)
				screen.SetManagedSecretAdopter(func(ctx context.Context, req llm.ManagedSecretAdoptRequest) (string, error) {
					put, putErr := b.security.Capabilities.Put(ctx, secretcap.PutRequest{
						ProjectID: req.ProjectID, ChatSessionID: req.RootSessionID, SessionID: req.SessionID,
						OperationID: req.OperationID, Name: req.Name, Purpose: req.Purpose,
						Scope: secretcap.ScopeChat, Origin: secretcap.OriginDetected, Value: req.Value,
					})
					return put.Metadata.Reference, putErr
				})
			}
			b.providers.Service.Registry.SetOutboundSecretScreen(screen)
		}
	}
	if err := b.server.MCP.Catalog.Load(b.startup.ctx); err != nil {
		return fmt.Errorf("mcp registry: %w", err)
	}
	if b.execution.Host != nil && b.execution.Host.Executor != nil {
		b.execution.Host.Executor.Rejections.SetMCPCatalog(b.server.MCP.Catalog)
	}
	if b.sessions != nil && b.sessions.Manager != nil {
		b.sessions.Manager.ToolPolicy.SetMCPRuntime(b.server.MCP.Catalog)
	}
	return nil
}

type watchTreeStopAdapter struct {
	store *watch.Store
}

func (a watchTreeStopAdapter) FenceWatches(ctx context.Context, sessionIDs []string) error {
	return a.store.FenceSessions(ctx, sessionIDs)
}

func (a watchTreeStopAdapter) ReconcileStoppedWatches(ctx context.Context, sessionIDs []string) (int, error) {
	summary, err := a.store.ReconcileStoppedSessions(ctx, sessionIDs...)
	return summary.CanceledCount, err
}

type derivedCommandWatchAdapter struct {
	store *watch.Store
}

func (a derivedCommandWatchAdapter) HandoffCommand(
	ctx context.Context,
	tc tools.ToolContext,
	commandHandle string,
	outputCursor int64,
) (command.DerivedCommandHandoff, bool, error) {
	if !a.store.Configured() {
		return command.DerivedCommandHandoff{}, false, nil
	}
	owner := watch.Owner{
		ProjectID:   tc.Identity.ProjectID,
		SessionID:   tc.Identity.SessionID,
		WorkerJobID: tc.Identity.WorkerJobID,
	}.Normalized()
	var rootPaths []string
	for _, root := range tc.Source.Roots {
		if root.Path != "" {
			rootPaths = append(rootPaths, root.Path)
		}
	}
	authority := watch.Authority{
		WorkspaceID:   tc.Source.ActiveRootID,
		RootPaths:     rootPaths,
		LoopbackPorts: append([]uint16(nil), tc.Local.LoopbackConnectPorts...),
	}
	h, err := a.store.HandoffDerivedCommand(ctx, owner, authority, commandHandle, outputCursor)
	if err != nil {
		return command.DerivedCommandHandoff{}, false, err
	}
	return command.DerivedCommandHandoff{
		Handle:        h.Handle,
		WatchHandle:   h.WatchHandle,
		WaitCondition: h.WaitCondition,
		OutputCursor:  h.OutputCursor,
	}, true, nil
}

func (a derivedCommandWatchAdapter) ReconcileShellJoin(ctx context.Context, tc tools.ToolContext, commandHandle string) error {
	if !a.store.Configured() {
		return nil
	}
	owner := watch.Owner{
		ProjectID:   tc.Identity.ProjectID,
		SessionID:   tc.Identity.SessionID,
		WorkerJobID: tc.Identity.WorkerJobID,
	}.Normalized()
	return a.store.ReconcileShellJoin(ctx, owner, commandHandle)
}
