package app

import (
	"context"
	"fmt"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/boot"
	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/parse"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (b *serveBuilder) wireCoordinatorRuntime() error {
	b.coordRuntime = coordinator.NewRuntime(b.mgr.CoordinatorRuntimeDeps())
	b.mgr.SetCoordinatorRuntime(b.coordRuntime)
	waitConditions := make(map[string]map[string]bool, len(b.toolProfiles))
	for _, profile := range b.toolProfiles {
		allowed := make(map[string]bool, len(profile.WaitConditions))
		for _, condition := range profile.WaitConditions {
			allowed[condition] = true
		}
		waitConditions[profile.ID] = allowed
	}
	waitStore := &awaitstore.Store{DB: b.db}
	if err := loopwake.RegisterWaitTool(b.toolRuntime.Registry, b.coordRuntime.CoordinatorLoop(), loopwake.WaitToolDeps{
		Store: waitStore, ProfileConditions: waitConditions,
		SecretMatcher: b.secretMatcher, RuntimeContext: b.ctx,
	}); err != nil {
		return fmt.Errorf("wait tool: %w", err)
	}
	if err := b.registerRecovery(bootrecovery.Entry{
		Name: "agent-wait-leases", Kind: bootrecovery.KindReconcile, Phase: bootrecovery.PhaseServe,
		Run: func(ctx context.Context) error {
			return loopwake.RecoverWaitLeases(ctx, b.coordRuntime.CoordinatorLoop(), waitStore)
		},
	}); err != nil {
		return err
	}

	if err := boot.ValidateServeWiring(boot.ServeWiring{
		PostureRegistry: b.postureRegistry,
		BundledRules:    b.bundledRules,
		RuleEngine:      b.ruleEngine,
		SessionManager:  b.mgr,
		WorkflowManager: b.workflowMgr,
	}); err != nil {
		return fmt.Errorf("serve wiring: %w", err)
	}
	return nil
}

func (b *serveBuilder) registerCoordinatorTools() error {
	if err := delegation.RegisterDelegationTools(b.toolRuntime.Registry, b.delegationMgr); err != nil {
		return fmt.Errorf("delegation tools: %w", err)
	}
	parseSvc := parse.NewDefaultService()
	if err := parse.RegisterParseTools(b.toolRuntime.Registry, parseSvc); err != nil {
		return fmt.Errorf("parse tools: %w", err)
	}
	if err := workflow.RegisterStateTools(b.toolRuntime.Registry, workflow.StateToolDeps{
		Runs:     b.workflowMgr,
		Sessions: b.store,
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
		Projects:     b.registry,
		Scans:        b.scanTriggers,
		SourceLedger: b.sourceLedger,
		DataDir:      b.dataDir,
		Reports: worker.ChangeReportDeps{
			SourceRuns: b.mgr.WorkerSourceRuns,
			Messages: func(ctx context.Context, childSessionID string) ([]wire.Message, error) {
				return b.store.GetMessages(ctx, childSessionID)
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
	if err := b.registerRecovery(bootrecovery.Entry{
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
	profileByID := make(map[string]sandbox.ToolProfile, len(b.toolProfiles))
	for _, p := range b.toolProfiles {
		profileByID[p.ID] = p
	}
	if err := orchestration.ValidateAgentSkillSurface(b.agentRegistry, profileByID); err != nil {
		return fmt.Errorf("agent skill surface: %w", err)
	}
	return nil
}

// taskToolDeps wires the task tool to the queue, the workflow's planned legs,
// and the worker assignment prompt.
func (b *serveBuilder) taskToolDeps() worker.TaskToolDeps {
	return worker.TaskToolDeps{
		Sessions:         b.mgr,
		Queue:            b.workerQueue,
		Agents:           b.agentRegistry,
		Workers:          b.workersCfg,
		ToolBudget:       b.workerToolBudgetFor,
		BindWorkflowTask: b.workflowMgr.BindWorkflowTask,
		WorkflowWork:     b.workflowMgr.WorkflowWork,
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
			msgs, err := b.store.GetMessages(ctx, tctx.SessionID)
			if err != nil {
				return brief.Goal, err
			}
			in := inject.WorkerTaskAssignmentInput{
				SessionID:        tctx.SessionID,
				ProjectDir:       tctx.ActiveRootPath(),
				Charter:          brief,
				AgentType:        agentType,
				WorkerJobID:      workerJobID,
				MaxToolLoops:     maxToolLoops,
				Attachments:      surface.SessionForwardedAttachments(msgs),
				RecordedVerdicts: recordedVerdictsForLeg(ctx, b.workflowMgr, tctx.SessionID),
			}
			if scope != nil {
				in.Scope = *scope
			}
			run, err := b.workflowMgr.GetActive(ctx, tctx.SessionID)
			if err != nil {
				return "", err
			}
			if run != nil {
				manifest, err := b.workflowMgr.ManifestForRunID(ctx, run.ID)
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
	stamped := mgr.StampedReviewVerdicts(ctx, sessionID)
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
