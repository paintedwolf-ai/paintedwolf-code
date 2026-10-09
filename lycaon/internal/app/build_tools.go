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

// toolWiring wires the coordinator tools, scanning, detection packs, and the OAR block plane.
type toolWiring struct{ *serveBuilder }

func (b toolWiring) wireCoordinatorRuntime() error {
	b.coordRuntime = coordinator.NewRuntime(b.mgr.CoordinatorRuntimeDeps())
	b.mgr.SetCoordinatorRuntime(b.coordRuntime)
	waitConditions := make(map[string]map[string]bool, len(b.agents.ToolProfiles))
	for _, profile := range b.agents.ToolProfiles {
		allowed := make(map[string]bool, len(profile.WaitConditions))
		for _, condition := range profile.WaitConditions {
			allowed[condition] = true
		}
		waitConditions[profile.ID] = allowed
	}
	waitStore := &awaitstore.Store{DB: b.storage.Database}
	if err := loopwake.RegisterWaitTool(b.toolRuntime.Registry, b.coordRuntime.CoordinatorLoop(), loopwake.WaitToolDeps{
		Store: waitStore, ProfileConditions: waitConditions,
		SecretMatcher: b.security.Matcher, RuntimeContext: b.startup.ctx,
	}); err != nil {
		return fmt.Errorf("wait tool: %w", err)
	}
	if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
		Name: "agent-wait-leases", Kind: bootrecovery.KindReconcile, Phase: bootrecovery.PhaseServe,
		Run: func(ctx context.Context) error {
			return loopwake.RecoverWaitLeases(ctx, b.coordRuntime.CoordinatorLoop(), waitStore)
		},
	}); err != nil {
		return err
	}

	if err := boot.ValidateServeWiring(boot.ServeWiring{
		PostureRegistry: b.agents.Postures,
		BundledRules:    b.bundledRules,
		RuleEngine:      b.ruleEngine,
		SessionManager:  b.mgr,
		WorkflowManager: b.workflowMgr,
	}); err != nil {
		return fmt.Errorf("serve wiring: %w", err)
	}
	return nil
}

func (b toolWiring) registerCoordinatorTools() error {
	if err := delegation.RegisterDelegationTools(b.toolRuntime.Registry, b.delegationMgr); err != nil {
		return fmt.Errorf("delegation tools: %w", err)
	}
	parseSvc := parse.NewDefaultService()
	if err := parse.RegisterParseTools(b.toolRuntime.Registry, parseSvc); err != nil {
		return fmt.Errorf("parse tools: %w", err)
	}
	if err := workflowstatetools.RegisterStateTools(b.toolRuntime.Registry, workflowstatetools.StateToolDeps{Runs: b.workflowMgr.Store.Runs, Vars: b.workflowMgr.Phases.Vars, Journal: b.workflowMgr.Phases.Journal, Resolver: &b.workflowMgr.Resolver, Starts: b.workflowMgr.Starts, Controls: b.workflowMgr.Controls, Scaffold: b.workflowMgr.Blueprints.Scaffold,
		Sessions: b.storage.Sessions,
	}); err != nil {
		return fmt.Errorf("state tools: %w", err)
	}
	if err := blueprint.RegisterPlanTools(b.toolRuntime.Registry, b.blueprintMgr); err != nil {
		return fmt.Errorf("plan tools: %w", err)
	}
	if err := worker.RegisterTaskTool(b.toolRuntime.Registry, b.taskToolDeps()); err != nil {
		return fmt.Errorf("task tool: %w", err)
	}
	if err := worker.RegisterAnswerDecisionTool(b.toolRuntime.Registry, worker.AnswerDecisionToolDeps{
		Answer: b.answerDecisionSvc,
	}); err != nil {
		return fmt.Errorf("answer_decision tool: %w", err)
	}
	if err := worker.RegisterExtendWorkerBudgetTool(b.toolRuntime.Registry, worker.ExtendBudgetToolDeps{
		Queue:      b.workerQueue,
		Ledger:     b.workerBudgetLedger,
		ToolBudget: b.workerToolBudgetFor,
	}); err != nil {
		return fmt.Errorf("extend_worker_budget tool: %w", err)
	}
	if err := worker.RegisterDeclineWorkerBudgetTool(b.toolRuntime.Registry, worker.DeclineBudgetToolDeps{
		Queue:  b.workerQueue,
		Ledger: b.workerBudgetLedger,
	}); err != nil {
		return fmt.Errorf("decline_worker_budget tool: %w", err)
	}
	b.toolRuntime.Boundary.SetMergeReconcileAllowlister(b.mgr)
	workerMergeSvc := &worker.MergeService{
		Queue:        b.workerQueue,
		Store:        b.workerQueue,
		Workspace:    b.wsMgr,
		Reject:       b.rejectFmt,
		Sessions:     b.workerQueue,
		Reconcile:    b.mgr,
		Coord:        b.mgr,
		Closeout:     b.delegationMgr,
		Projects:     b.storage.Projects,
		Scans:        b.scanTriggers,
		SourceLedger: b.storage.SourceLedger,
		DataDir:      b.storage.Directory,
		Reports: worker.ChangeReportDeps{
			SourceRuns: b.mgr.WorkerSourceRuns,
			Messages: func(ctx context.Context, childSessionID string) ([]wire.Message, error) {
				return b.storage.Sessions.GetMessages(ctx, childSessionID)
			},
		},
	}
	workerMergeSvc.Evidence = worker.SourceEvidenceContext{
		SourceRevision: b.mgr.WorkerVerificationRevision,
		DeclaredCommand: func(ctx context.Context, task *wire.WorkerTask) string {
			if task == nil {
				return ""
			}
			return b.mgr.SourceVerifyCommand(ctx, task.WorkspacePath)
		},
	}
	b.workerMergeSvc = workerMergeSvc
	if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
		// Reconcile database and worktree state under the lease after graph wiring.
		Name: "worker-merge-applies", Kind: bootrecovery.KindReconcile, Phase: bootrecovery.PhaseServe,
		Run: workerMergeSvc.RecoverOrphanedMergeApplies,
	}); err != nil {
		return err
	}
	if err := worker.RegisterWorkerCancelTool(b.toolRuntime.Registry, worker.CancelToolDeps{
		Cancel: b.workerCancelSvc,
	}); err != nil {
		return fmt.Errorf("worker_cancel tool: %w", err)
	}
	b.mgr.SetOverlayPromoter(workerMergeSvc)
	if err := worker.RegisterOverlayTools(b.toolRuntime.Registry, worker.OverlayToolDeps{
		Merge: workerMergeSvc,
	}); err != nil {
		return fmt.Errorf("overlay tools: %w", err)
	}
	if err := tools.ValidateBootToolClaimsHonest(b.toolRuntime.Registry); err != nil {
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
func (b toolWiring) taskToolDeps() worker.TaskToolDeps {
	return worker.TaskToolDeps{
		Sessions:         b.mgr,
		Queue:            b.workerQueue,
		Agents:           b.agents.Registry,
		Workers:          b.workersCfg,
		ToolBudget:       b.workerToolBudgetFor,
		BindWorkflowTask: b.workflowMgr.Fanout.BindWorkflowTask,
		WorkflowWork:     b.workflowMgr.Fanout.WorkflowWork,
		TaskReceipt:      b.workerQueue.TaskReceipt,
		PendingDecision: func(ctx context.Context, childSessionID string) (string, bool, error) {
			if b.mgr == nil || b.mgr.Decisions() == nil {
				return "", false, nil
			}
			dec, ok, err := b.mgr.Decisions().Get(ctx, childSessionID)
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
				RecordedVerdicts: recordedVerdictsForLeg(ctx, b.workflowMgr, tctx.Identity.SessionID),
			}
			if scope != nil {
				in.Scope = *scope
			}
			run, err := b.workflowMgr.Store.Runs.ActiveBySession(ctx, tctx.Identity.SessionID)
			if err != nil {
				return "", err
			}
			if run != nil {
				manifest, err := b.workflowMgr.Resolver.ForRunID(ctx, run.ID)
				if err != nil {
					return "", err
				}
				in.CoverageAssignment, err = b.workflowMgr.Coverage.CoverageAssignment(ctx, run, manifest, agentType)
				if err != nil {
					return "", err
				}
				if def, ok := manifest.PhaseByID(run.CurrentPhase); ok && def.ReviewLoop != nil && def.ReviewLoop.IncludeScanInventory {
					inventory, err := scan.WorkflowAdvisoryInventory(ctx, b.scanStore, run.ID)
					if err != nil {
						return "", err
					}
					in.ScanInventory = inventory
				}
			}
			return inject.RenderWorkerTaskAssignment(ctx, b.injectRenderer, in)
		},
		WebSearchEnabled: func() bool {
			if b.webResearchRuntime.Config == nil {
				return true
			}
			return b.webResearchRuntime.Config.SearchEnabled()
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

func (b toolWiring) wireMCP() error {
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
	b.mcpReg, err = mcp.NewRuntime(mcpOpts)
	if err != nil {
		return fmt.Errorf("mcp registry: %w", err)
	}
	b.security.BindMCPInventory(b.mcpReg.Catalog)
	b.mcpReg.Tools.SetToolRegistry(b.toolRuntime.Registry)
	b.mcpReg.Connections.SetAPIAccess(b.apiToken)
	if b.toolRuntime != nil {
		b.toolRuntime.Authority.SetMCPToolPinSource(b.mcpReg.Tools)
	}
	b.mcpReg.Catalog.SetProjectOverlayGate(b.settings.ProjectSurfaceGate(projectcontrib.SurfaceProjectMCP, b.storage.Projects).AppliesPath)
	if err := serverWiring(b).wireDestinationConfig(); err != nil {
		return err
	}
	// Device inspection may inspect every registered project root.
	b.mcpReg.Connections.SetDeviceProbeRoots(func() []string {
		paths, err := boardWiring(b).projectRootPaths(b.startup.ctx)
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
		if b.bgRegistry != nil {
			b.bgRegistry.SetCaptureProjector(captureProjector)
		}
		if b.previewCtrl != nil {
			b.previewCtrl.SetCaptureProjector(captureProjector)
		}
		if b.browserPool != nil {
			b.browserPool.SetCaptureProjector(captureProjector)
		}
		if b.browserRaster != nil {
			b.browserRaster.SetCaptureProjector(captureProjector)
		}
		b.mcpReg.Calls.SetSecretScreen(matcher, b.security.Ask(b.toolRuntime.Executor.Secrets, b.toolRuntime.Authority.ApprovalsDisabled))
		if b.toolRuntime != nil && b.toolRuntime.Executor != nil {
			b.toolRuntime.Executor.Secrets.SetSecretMatcher(matcher)
			b.toolRuntime.Executor.Secrets.SetSecretIgnores(b.security.Ignores)
		}
		// Editor spans preview outbound screening.
		b.security.Spans = secretspan.New(matcher)
		b.security.BindTranscript(matcher, b.mgr.SetMessageStorageRedactor, b.mgr.SweepSessionTree)
		if b.providers.Service != nil && b.providers.Service.Registry != nil {
			screen := llm.NewModelSecretScreen(matcher, b.security.Ask(b.toolRuntime.Executor.Secrets, b.toolRuntime.Authority.ApprovalsDisabled))
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
	if err := b.mcpReg.Catalog.Load(b.startup.ctx); err != nil {
		return fmt.Errorf("mcp registry: %w", err)
	}
	if b.toolRuntime != nil && b.toolRuntime.Executor != nil {
		b.toolRuntime.Executor.Rejections.SetMCPCatalog(b.mcpReg.Catalog)
	}
	if b.mgr != nil {
		b.mgr.SetMCPRuntime(b.mcpReg.Catalog)
	}
	return nil
}
