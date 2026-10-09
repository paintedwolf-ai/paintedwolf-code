package app

import (
	"context"
	"fmt"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/boot"
	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/parse"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretspan"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowstatetools "github.com/lycaon/lycaon/internal/workflow/statetools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (b *serveBuilder) wireCoordinatorRuntime() error {
	b.server.Coordinator = b.sessions.Manager.Coordinator.Runtime
	waitConditions := make(map[string]map[string]bool, len(b.agents.ToolProfiles))
	for _, profile := range b.agents.ToolProfiles {
		allowed := make(map[string]bool, len(profile.WaitConditions))
		for _, condition := range profile.WaitConditions {
			allowed[condition] = true
		}
		waitConditions[profile.ID] = allowed
	}
	waitStore := &awaitstore.Store{DB: b.storage.Database}
	if err := loopwake.RegisterWaitTool(b.execution.Host.Registry, b.server.Coordinator.CoordinatorLoop(), loopwake.WaitToolDeps{
		Store: waitStore, ProfileConditions: waitConditions,
		SecretMatcher: b.security.Matcher, RuntimeContext: b.startup.ctx,
	}); err != nil {
		return fmt.Errorf("wait tool: %w", err)
	}
	if err := b.registerRecovery(bootrecovery.Entry{
		Name: "agent-wait-leases", Kind: bootrecovery.KindReconcile, Phase: bootrecovery.PhaseServe,
		Run: func(ctx context.Context) error {
			return loopwake.RecoverWaitLeases(ctx, b.server.Coordinator.CoordinatorLoop(), waitStore)
		},
	}); err != nil {
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

func (b *serveBuilder) registerCoordinatorTools() error {
	if err := delegation.RegisterDelegationTools(b.execution.Host.Registry, b.delegations.Manager); err != nil {
		return fmt.Errorf("delegation tools: %w", err)
	}
	parseSvc := parse.NewDefaultService()
	if err := parse.RegisterParseTools(b.execution.Host.Registry, parseSvc); err != nil {
		return fmt.Errorf("parse tools: %w", err)
	}
	if err := workflowstatetools.RegisterStateTools(b.execution.Host.Registry, workflowstatetools.StateToolDeps{Runs: b.workflows.Store.Runs, Vars: b.workflows.Manager.Phases.Vars, Journal: b.workflows.Manager.Phases.Journal, Resolver: &b.workflows.Manager.Resolver, Starts: b.workflows.Manager.Starts, Controls: b.workflows.Manager.Controls, Scaffold: b.workflows.Manager.Blueprints.Scaffold,
		Sessions: b.storage.Sessions,
	}); err != nil {
		return fmt.Errorf("state tools: %w", err)
	}
	if err := blueprint.RegisterPlanTools(b.execution.Host.Registry, b.workflows.Blueprints); err != nil {
		return fmt.Errorf("plan tools: %w", err)
	}
	if err := worker.RegisterTaskTool(b.execution.Host.Registry, taskToolDeps(b)); err != nil {
		return fmt.Errorf("task tool: %w", err)
	}
	if err := worker.RegisterAnswerDecisionTool(b.execution.Host.Registry, worker.AnswerDecisionToolDeps{
		Answer: b.boards.AnswerDecision,
	}); err != nil {
		return fmt.Errorf("answer_decision tool: %w", err)
	}
	if err := worker.RegisterExtendWorkerBudgetTool(b.execution.Host.Registry, worker.ExtendBudgetToolDeps{
		Queue:      b.delegations.Queue,
		Ledger:     b.boards.BudgetLedger,
		ToolBudget: b.sessions.WorkerToolBudgetFor,
	}); err != nil {
		return fmt.Errorf("extend_worker_budget tool: %w", err)
	}
	if err := worker.RegisterDeclineWorkerBudgetTool(b.execution.Host.Registry, worker.DeclineBudgetToolDeps{
		Queue:  b.delegations.Queue,
		Ledger: b.boards.BudgetLedger,
	}); err != nil {
		return fmt.Errorf("decline_worker_budget tool: %w", err)
	}
	b.execution.Host.Boundary.SetMergeReconcileAllowlister(b.sessions.Manager)
	workerMergeSvc := &worker.MergeService{
		Queue:        b.delegations.Queue,
		Store:        b.delegations.Queue,
		Workspace:    b.delegations.Workspace,
		Reject:       b.execution.Rejections,
		Sessions:     b.delegations.Queue,
		Reconcile:    b.sessions.Manager,
		Coord:        b.sessions.Manager,
		Closeout:     b.delegations.Manager,
		Projects:     b.storage.Projects,
		Scans:        b.scanning.Triggers,
		SourceLedger: b.storage.SourceLedger,
		DataDir:      b.storage.Directory,
		Reports: worker.ChangeReportDeps{
			SourceRuns: b.sessions.Manager.Workers.Workspaces.SourceRuns,
			Messages: func(ctx context.Context, childSessionID string) ([]wire.Message, error) {
				return b.storage.Sessions.GetMessages(ctx, childSessionID)
			},
		},
	}
	workerMergeSvc.Evidence = worker.SourceEvidenceContext{
		SourceRevision: b.sessions.Manager.Workers.Workspaces.VerificationRevision,
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
		Cancel: b.delegations.Cancel,
	}); err != nil {
		return fmt.Errorf("worker_cancel tool: %w", err)
	}
	b.sessions.Manager.ProjectControl.SetOverlayPromoter(workerMergeSvc)
	if err := worker.RegisterOverlayTools(b.execution.Host.Registry, worker.OverlayToolDeps{
		Merge: workerMergeSvc,
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
		Sessions:         b.sessions.Manager,
		Queue:            b.delegations.Queue,
		Agents:           b.agents.Registry,
		Workers:          b.worker.cfg,
		ToolBudget:       b.sessions.WorkerToolBudgetFor,
		BindWorkflowTask: b.workflows.Manager.Fanout.BindWorkflowTask,
		WorkflowWork:     b.workflows.Manager.Fanout.WorkflowWork,
		TaskReceipt:      b.delegations.Queue.TaskReceipt,
		PendingDecision: func(ctx context.Context, childSessionID string) (string, bool, error) {
			if b.sessions.Manager == nil || b.sessions.Manager.Decisions == nil {
				return "", false, nil
			}
			dec, ok, err := b.sessions.Manager.Decisions.Get(ctx, childSessionID)
			if err != nil {
				return "", false, err
			}
			if !ok {
				return "", false, nil
			}
			return dec.WorkerID, true, nil
		},
		ComposePrompt: func(ctx context.Context, tctx tools.ToolContext, agentType string, brief wire.WorkerTaskCharter, workerJobID string, scope *wire.TaskScope, maxToolLoops int) (string, error) {
			msgs, err := b.storage.Sessions.GetMessages(ctx, tctx.Identity.SessionID)
			if err != nil {
				return brief.Goal, err
			}
			in := inject.WorkerTaskAssignmentInput{
				SessionID:        tctx.Identity.SessionID,
				ProjectDir:       tctx.ActiveRootPath(),
				Charter:          brief,
				AgentType:        agentType,
				WorkerJobID:      workerJobID,
				MaxToolLoops:     maxToolLoops,
				Attachments:      surface.SessionForwardedAttachments(msgs),
				RecordedVerdicts: recordedVerdictsForLeg(ctx, b.workflows.Manager, tctx.Identity.SessionID),
			}
			if scope != nil {
				in.Scope = *scope
			}
			run, err := b.workflows.Store.Runs.ActiveBySession(ctx, tctx.Identity.SessionID)
			if err != nil {
				return "", err
			}
			if run != nil {
				manifest, err := b.workflows.Manager.Resolver.ForRunID(ctx, run.ID)
				if err != nil {
					return "", err
				}
				in.CoverageAssignment, err = b.workflows.Manager.Coverage.CoverageAssignment(ctx, run, manifest, agentType)
				if err != nil {
					return "", err
				}
				if def, ok := manifest.PhaseByID(run.CurrentPhase); ok && def.ReviewLoop != nil && def.ReviewLoop.IncludeScanInventory {
					inventory, err := scan.WorkflowAdvisoryInventory(ctx, b.scanning.Store, run.ID)
					if err != nil {
						return "", err
					}
					in.ScanInventory = inventory
				}
			}
			return inject.RenderWorkerTaskAssignment(ctx, b.delegations.InjectRenderer, in)
		},
		WebSearchEnabled: func() bool {
			if b.boards == nil || b.boards.WebRuntime.Config == nil {
				return true
			}
			return b.boards.WebRuntime.Config.SearchEnabled()
		},
	}
}

// recordedVerdictsForLeg projects stamped verdicts onto the assignment DTO.
// The mapping lives here because inject cannot import workflow — workflow
// already imports inject.
func recordedVerdictsForLeg(ctx context.Context, mgr *workflow.RunManager, sessionID string) []inject.RecordedVerdict {
	stamped := mgr.Verdicts.StampedReviewVerdicts(ctx, sessionID)
	if len(stamped) == 0 {
		return nil
	}
	out := make([]inject.RecordedVerdict, 0, len(stamped))
	for _, v := range stamped {
		fields := make([]inject.RecordedVerdictField, 0, len(v.Fields))
		for _, f := range v.Fields {
			fields = append(fields, inject.RecordedVerdictField{Name: f.Name, Value: f.Value})
		}
		out = append(out, inject.RecordedVerdict{
			Phase:       v.Phase,
			EvidenceKey: v.EvidenceKey,
			Fields:      fields,
		})
	}
	return out
}

func (b *serveBuilder) wireMCP() error {
	mcpOpts := mcp.RuntimeOptions{
		OnSettingsChange: func() {
			if b.events.Hub != nil {
				_ = b.events.Hub.Publish(b.startup.ctx, wire.EventTopicSettings, events.PublishKey{Facet: string(wire.SettingsAreaMcp)}, wire.SettingsEvent{
					Area:   wire.SettingsAreaMcp,
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
	if matcher, err := b.security.LoadMatcher(b.startup.cfg.TestSecretMatcher); err != nil {
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
			b.interactions.Processes.SetCaptureProjector(captureProjector)
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
		b.security.BindTranscript(matcher, b.sessions.Manager.Runner.Transcript.SetRedactor, b.sessions.Manager.Runner.Transcript.SweepSessionTree)
		if b.providers.Service != nil && b.providers.Service.Registry != nil {
			screen := llm.NewModelSecretScreen(matcher, b.security.Ask(b.execution.Host.Executor.Secrets, b.execution.Host.Authority.ApprovalsDisabled))
			if b.security.Capabilities != nil {
				screen.SetManagedSecretEvidence(b.security.Capabilities.ScreeningValues)
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
