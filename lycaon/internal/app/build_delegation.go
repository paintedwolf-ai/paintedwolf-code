package app

import (
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	workflowruntime "github.com/lycaon/lycaon/internal/workflow/runtime"

	"context"
	"fmt"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	"log/slog"
	"strings"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/reenter"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/internal/workspace"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// delegationWiring wires delegation workers, workflow hooks, and background runners.
type delegationWiring struct{ *serveBuilder }

func (b delegationWiring) wireDelegationWorkers() error {
	if err := b.wireWorkerServices(); err != nil {
		return err
	}
	if err := b.configureDelegationWorkflow(); err != nil {
		return err
	}
	return b.wireWorkerContext()
}

func (b delegationWiring) wireWorkerServices() error {
	b.criteriaChecker = &delegation.GitInspectorCriteriaChecker{
		Git:       b.gitMgr,
		Inspector: b.simpleInspector,
		Store:     b.delegationStore,
	}

	b.injectRenderer = prompts.NewInjectRenderer(b.promptEngine)
	b.workerExec = worker.NewLocalWorkerExecutor(b.mgr, b.workerQueue)
	b.workerExec.Waits = &awaitstore.Store{DB: b.db}
	b.workerQueue.SetSessionAdmission(b.mgr.WithSessionTreeAdmission)
	b.workerExec.SetPromptInjects(b.injectRenderer)
	b.workerExec.SetPhaseTouchPaths(b.workflowMgr.Ambient)
	b.workerBranchRoot = enginepaths.WorkerBranchesRootUnder(b.dataDir)
	b.workerSeedRoot = enginepaths.WorkerSeedsRootUnder(b.dataDir)
	b.wsMgr = workspace.NewManager(b.workerBranchRoot, b.workerSeedRoot)
	b.workerQueue.SetWorkerWorkspaceManager(b.wsMgr)
	b.workerQueue.SetBaselineStore(b.sourceLedger.BaselineStore())
	b.workerQueue.SetProjectStore(b.registry)

	b.workerCancelSvc = &worker.CancelService{
		Queue:    b.workerQueue,
		Sessions: b.mgr,
		Reject:   b.rejectFmt,
		Reports: worker.ChangeReportDeps{
			Messages: func(ctx context.Context, childSessionID string) ([]wire.Message, error) {
				return b.store.GetMessages(ctx, childSessionID)
			},
		},
	}
	b.workflowMgr.Controls.Cleanup.Workers = &worker.RunStopService{
		Queue:       b.workerQueue,
		Sessions:    b.mgr,
		Reports:     b.workerCancelSvc.Reports,
		Delegations: b.delegationStore,
	}
	b.workflowMgr.Recovery.Busy = func(ctx context.Context, sessionID string) bool {
		sess, err := b.store.Get(ctx, sessionID)
		if err != nil || sess == nil {
			return false
		}
		return sess.Status == wire.SessionStatusBusy
	}
	b.workerExec.Reports = b.workerCancelSvc.Reports
	b.workerQueue.SetCancellationReports(b.workerCancelSvc.Reports)

	dispatchGate := delegation.ImplementWorkflowDispatchGate{
		Inner: delegation.WorkflowDispatchGate{
			Inner: delegation.AllowGate{},
			Store: b.delegationStore,
			Runs:  b.workflowMgr.Policy,
		},
		Store: b.delegationStore,
		Plans: b.blueprintMgr,
		WorkflowReady: workflow.RegistryWorkflowReadyChecker{
			Registry: b.condReg,
			Sessions: b.store,
		},
	}
	b.delegationMgr = delegation.NewManager(b.delegationStore, b.workerQueue, b.mgr, dispatchGate)
	b.delegationMgr.Projects = b.registry
	b.delegationMgr.Plans = b.blueprintMgr
	b.delegationMgr.HeadSHA = b.gitMgr
	b.delegationMgr.InspectorCloseout = &delegation.InspectorCloseoutGate{
		Inspector: b.simpleInspector,
		Store:     b.delegationStore,
		Security:  b.securityCloseout,
	}
	return nil
}

func (b delegationWiring) configureDelegationWorkflow() error {
	b.mgr.SetWorkflowDomains(&session.WorkflowDomains{Runs: b.workflowMgr.Store.Runs, Policy: b.workflowMgr.Policy, Ambient: b.workflowMgr.Ambient, Blueprints: b.workflowMgr.Blueprints, Batch: b.workflowMgr.Batch, Slash: b.workflowMgr.Slash, Requests: b.workflowMgr.Requests, Feedback: b.workflowMgr.Feedback, Transcript: b.workflowMgr.Transcript, Asks: b.workflowMgr.Asks, Fanout: b.workflowMgr.Fanout, Phases: b.workflowMgr.Phases, Reports: b.workflowMgr.Reports, Recovery: b.workflowMgr.Recovery, Cleanup: b.workflowMgr})
	b.mgr.SetWorkflowToolAccessView(b.workflowMgr.Policy)
	b.mgr.SetSessionWorkflowStop(b.workflowMgr.Controls)
	b.workflowMgr.Starts.Barrier = b.mgr
	b.workflowMgr.Controls.SessionExit = b.mgr
	b.workflowMgr.Requests.OnRequestAccepted = b.mgr.CurateAcceptedWorkflowRequest
	if b.hintCfg == nil {
		return fmt.Errorf("hint registry: not loaded")
	}
	gateCfg, gateErr := feedback.LoadGateFeedbackCatalog()
	if gateErr != nil {
		return fmt.Errorf("gate feedback: %w", gateErr)
	}
	b.mgr.SetWorkflowHints(b.hintCfg, gateCfg)
	if evidenceBinding, err := evidence.LoadBinding(); err != nil {
		return fmt.Errorf("evidence binding: %w", err)
	} else {
		evidence.SetBinding(evidenceBinding)
	}
	b.mgr.SetWorkspaceChecker(&workercompletion.CompositeWorkspaceChangeChecker{
		Git: &workercompletion.GitWorkspaceChangeChecker{Git: b.gitMgr},
	})
	if err := b.mgr.InstallAnchorRegistry(); err != nil {
		return fmt.Errorf("anchor registry: %w", err)
	}
	b.mgr.SetLoopWorkflowSource(&loopwake.WorkflowDomains{Runs: b.workflowMgr.Store.Runs, Approvals: b.workflowMgr.Policy, Obligations: b.workflowMgr.Obligations})
	b.workflowMgr.Phases.PhaseEnterHook = b.onWorkflowPhaseEnter
	b.workflowMgr.Phases.PhaseReenterHook = b.onWorkflowPhaseReenter
	b.workflowMgr.Publication.OnPhaseAutoAdvanced = b.onWorkflowPhaseAutoAdvanced
	b.workflowMgr.Controls.OnRunResumed = b.onWorkflowRunResumed
	b.workflowMgr.Children.OnRunCompleted = b.onWorkflowRunCompleted
	b.workflowMgr.Approvals.OnHumanApprovalAdvanced = b.onWorkflowHumanApprovalAdvanced
	b.workflowMgr.Feedback.OnFeedbackPending = b.onWorkflowFeedbackPending
	b.workflowMgr.Asks.OnToolAskOpened = b.onWorkflowToolAskOpened
	b.workflowMgr.Feedback.OnFeedbackResolved = b.onWorkflowFeedbackResolved
	b.workflowMgr.Verdicts.OnReviewLoopHeld = b.onWorkflowReviewLoopHeld
	b.delegationMgr.OnCloseout = b.onDelegationCloseout
	b.mgr.SetCoordinatorTurnFrameSource(&workflowruntime.CoordinatorFrames{
		Runs: b.workflowMgr.Store.Runs, Resolver: &b.workflowMgr.Resolver, Snapshots: b.workflowMgr.Snapshots, Policy: b.workflowMgr.Policy, Obligations: b.workflowMgr.Obligations,
		SessionStore:   b.sessionWorkflowStore,
		ConfigRoot:     b.configRoot,
		VerdictCatalog: b.sessionVerdictCatalog,
	})
	if b.synthesisCurator != nil {
		b.mgr.SetSynthesisCurator(b.synthesisCurator)
	}
	return nil
}

func (b delegationWiring) wireWorkerContext() error {
	personaContract, err := prompts.LoadPersonaContract()
	if err != nil {
		return fmt.Errorf("persona contract: %w", err)
	}
	b.playbookMatcher, err = prompts.LoadPlaybookMatcherEffective(personaContract)
	if err != nil {
		return fmt.Errorf("playbooks: %w", err)
	}
	legToolLister := delegation.LegToolListerFunc(func(ctx context.Context, sess *wire.Session, profileID string) []string {
		policy := b.mgr.PromptToolPolicy()
		if policy == nil || sess == nil {
			return nil
		}
		var names []string
		for _, meta := range policy.ListForPrompt(ctx, sess, profileID) {
			if strings.TrimSpace(meta.Name) != "" {
				names = append(names, meta.Name)
			}
		}
		return names
	})
	agentsForSession := func(sess *wire.Session) session.AgentProfileResolver {
		if view := b.mgr.Catalog().ViewForSession(context.Background(), sess); view != nil {
			return view
		}
		return b.agentRegistry
	}
	playbooksForSession := func(sess *wire.Session) delegation.PlaybookMatcherInterface {
		if view := b.mgr.Catalog().ViewForSession(context.Background(), sess); view != nil && view.Playbooks != nil {
			return view.Playbooks
		}
		return b.playbookMatcher
	}
	b.mgr.SetWorkerContextBuilder(&delegation.CompositeWorkerContext{
		Delegation: &delegation.WorkerContextLoader{
			Store:         b.delegationStore,
			Tasks:         b.workerQueue,
			Runs:          b.workflowMgr.Phases,
			MatcherFor:    playbooksForSession,
			AgentsFor:     agentsForSession,
			Tools:         legToolLister,
			Scans:         b.scanCoordinator,
			AgentsMDChain: b.mgr.AgentsMDChainForPaths,
			Repo:          b.repoProvider,
			Topology: func(workflowID string) string {
				if strings.TrimSpace(workflowID) == "default-pipeline" {
					return "pipeline"
				}
				return strings.TrimSpace(workflowID)
			},
		},
		Tools:      legToolLister,
		AgentsFor:  agentsForSession,
		MatcherFor: playbooksForSession,
	})
	return nil
}

func (b delegationWiring) onWorkflowPhaseEnter(ctx context.Context, rc *workflowphases.RunContext, def workflowdef.PhaseDef) {
	if rc == nil {
		return
	}
	b.mgr.BeginWorkflowPhase(ctx, rc.SessionID)
	// Bind progress before checklist writes on this phase.
	if b.progressStore != nil && progress.AdoptActiveRun(ctx, b.progressStore, rc.SessionID, rc.RunID, "") {
		progress.NotifyWriteObservers(ctx, progress.WriteEvent{SessionID: rc.SessionID})
	}
	// Evaluate topology gates against the entering phase.
	if !rc.IsRunStart() && (strings.TrimSpace(def.BindTopologyStage) != "" || len(def.BindParallelGroup) > 0) {
		if run, err := b.workflowMgr.Store.Runs.Get(ctx, rc.RunID); err == nil && run != nil {
			run.CurrentPhase = rc.Phase
			b.srv.Workflow.StartOrchestratedTopologyForRun(ctx, rc.SessionID, run)
		}
	}
	// Resolve phase-entry guidance through workflow bindings.
	env := anchor.Envelope{}
	if vars, err := b.workflowMgr.Policy.ScaffoldVarsForSession(ctx, rc.SessionID); err == nil {
		if digest, ok := vars["evidence_digest"].(string); ok && strings.TrimSpace(digest) != "" {
			env.EvidenceDigest = strings.TrimSpace(digest)
		}
		if out, ok := vars["topology_outputs"].(map[string]any); ok {
			for _, key := range []string{"fan_out", "pack"} {
				if text, _ := out[key].(string); strings.TrimSpace(text) != "" {
					env.TopologyOutput = strings.TrimSpace(text)
					break
				}
			}
		}
		if crit, ok := runstate.DotPathString(vars, "options.criterion"); ok {
			env.DesignForkCriterion = crit
		}
		if obligations := workflow.ObligationsFromVars(vars); obligations != nil {
			if env.Vars == nil {
				env.Vars = map[string]any{}
			}
			env.Vars["obligations"] = obligations
		}
		if spawnable, ok := runstate.ReviewIfSpawnableSnapshot(vars, rc.Phase); ok && len(spawnable) > 0 {
			if env.Vars == nil {
				env.Vars = map[string]any{}
			}
			env.Vars["spawnable_reviewers"] = spawnable
		}
		if verdicts, ok := vars["review_verdict"].(map[string]any); ok && len(verdicts) > 0 {
			if env.Vars == nil {
				env.Vars = map[string]any{}
			}
			env.Vars["review_verdict"] = verdicts
		}
		if manifest, err := b.workflowMgr.Resolver.ForRunID(ctx, rc.RunID); err == nil {
			if phase, ok := manifest.PhaseByID(rc.Phase); ok {
				if plan, found := runstate.FanoutPlanForPhase(vars, phase); found {
					env.FanoutPlanText = runstate.FormatFanoutPlan(plan)
				}
				env.MaxFanoutLegs = workflow.FanoutPlanMaxLegsForPhase(manifest, phase)
				if env.Vars == nil {
					env.Vars = map[string]any{}
				}
				if brief := manifest.ReportBrief(); brief != nil {
					env.Vars["rating_questions"] = brief.PromptText()
				}
				if rl := phase.ReviewLoop; rl != nil && len(rl.ClaimStatuses) > 0 {
					env.Vars["claim_statuses"] = rl.StatusWords()
					env.Vars["review_followup_attempts"] = rl.FollowupAttempts
				}
			}
		}
	}
	b.mgr.EmitMatch(ctx, rc.SessionID, anchor.PhaseEntered, env, anchor.MatchContext{
		Surface:  "phase",
		Phase:    rc.Phase,
		Workflow: rc.WorkflowID,
	})
	// Host-held phases park the coordinator.
	heldByHost := false
	if def.MayHostHold() {
		held, err := b.workflowMgr.Obligations.HostObligationHeld(ctx, rc.SessionID)
		heldByHost = err == nil && held
	}
	if heldByHost {
		// Run start completes its topology hook before parking.
		if !rc.IsRunStart() {
			b.mgr.CancelInFlightPrompt(rc.SessionID)
		}
		b.coordRuntime.CoordinatorLoop().ParkForHostObligation(ctx, rc.SessionID)
	}
	if mode := workflowdef.ForceExecutionMode(def.OnEnter.SetExecutionMode); mode != "" {
		b.mgr.PushExecutionModeTransitionCause(rc.SessionID, surface.ModeTransitionCause{
			Kind: surface.ModeTransitionCausePhaseHook,
			Mode: mode,
		})
	} else if manifest, err := b.workflowMgr.Resolver.ForRunID(ctx, rc.RunID); err == nil {
		if vars, err := b.workflowMgr.Policy.ScaffoldVarsForSession(ctx, rc.SessionID); err == nil {
			if mode, ok := workflowdef.WorkflowDefaultForceMode(manifest, vars); ok {
				b.mgr.PushExecutionModeTransitionCause(rc.SessionID, surface.ModeTransitionCause{
					Kind: surface.ModeTransitionCauseWorkflowDefault,
					Mode: mode,
				})
			}
		}
	}
}

func (b delegationWiring) onWorkflowPhaseReenter(ctx context.Context, rc *workflowphases.RunContext, def workflowdef.PhaseDef) {
	if rc == nil {
		return
	}
	kickID := strings.TrimSpace(def.OnReenter.InjectKick)
	if kickID == "" {
		return
	}
	id, ok := anchor.ParseID(kickID)
	if !ok {
		slog.WarnContext(ctx, "phase re-enter kick is not a catalog anchor", "session_id", rc.SessionID, "phase", def.ID, "kick", kickID)
		return
	}
	b.mgr.Emit(ctx, rc.SessionID, id, b.mgr.CoordinatorEnvelopeForWorkerCycleTerminal(ctx, rc.SessionID, ""))
}

func (b delegationWiring) onWorkflowPhaseAutoAdvanced(ctx context.Context, sessionID, runID, previousPhase, newPhase string) {
	run, runErr := b.workflowMgr.Store.Runs.Get(context.WithoutCancel(ctx), runID)
	if runErr != nil || run == nil || run.Status != wire.WorkflowRunStatusRunning {
		return
	}

	manifest, err := b.workflowMgr.Resolver.ForRunID(ctx, runID)
	if err == nil {
		if _, ok := workflowphases.ReenterLegForAdvance(manifest, previousPhase, newPhase, sessionID); ok {
			reenter.NudgeOnManifestReenter(ctx, b.mgr, sessionID, manifest, previousPhase, newPhase)
			return
		}
	}
	// Human starts enter the first phase from an empty previous phase.
	if strings.TrimSpace(previousPhase) == "" && strings.TrimSpace(newPhase) != "" {
		postStartCtx := context.WithoutCancel(ctx)
		if run, getErr := b.workflowMgr.Store.Runs.Get(postStartCtx, runID); getErr == nil && run != nil {
			b.srv.Workflow.StartOrchestratedTopologyForRun(postStartCtx, sessionID, run)
		}
		if held, heldErr := b.workflowMgr.Obligations.HostObligationHeld(postStartCtx, sessionID); heldErr == nil && held {
			b.mgr.CancelInFlightPrompt(sessionID)
			b.coordRuntime.CoordinatorLoop().ParkForHostObligation(postStartCtx, sessionID)
			return
		}
		b.mgr.NudgeCoordinatorLoop(ctx, sessionID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
		return
	}
	// Host transitions wake the newly entered phase.
	if strings.TrimSpace(previousPhase) != strings.TrimSpace(newPhase) {
		postAdvanceCtx := context.WithoutCancel(ctx)
		if held, heldErr := b.workflowMgr.Obligations.HostObligationHeld(postAdvanceCtx, sessionID); heldErr == nil && held {
			b.coordRuntime.CoordinatorLoop().ParkForHostObligation(postAdvanceCtx, sessionID)
			return
		}
		b.mgr.NudgeCoordinatorLoop(postAdvanceCtx, sessionID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
		return
	}
	b.mgr.NudgeCoordinatorLoop(ctx, sessionID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
}

func (b delegationWiring) onWorkflowRunCompleted(ctx context.Context, run *wire.WorkflowRun) {
	if run == nil || run.Status != wire.WorkflowRunStatusComplete {
		return
	}
	if err := b.mgr.SettleCompletedWorkflow(ctx, run.SessionID, run.ID); err != nil {
		slog.ErrorContext(ctx, "settle completed workflow", "session_id", run.SessionID, "run_id", run.ID, "error", err)
	}
}

func (b delegationWiring) onWorkflowRunResumed(ctx context.Context, run *wire.WorkflowRun) {
	active, err := b.workflowMgr.Store.Runs.ActiveBySession(ctx, run.SessionID)
	if err != nil || active == nil || active.ID != run.ID || active.Status != wire.WorkflowRunStatusRunning {
		return
	}
	if held, heldErr := b.workflowMgr.Obligations.HostObligationHeld(ctx, run.SessionID); heldErr == nil && held {
		b.coordRuntime.CoordinatorLoop().ParkForHostObligation(ctx, run.SessionID)
		return
	}
	b.mgr.NudgeCoordinatorLoop(ctx, run.SessionID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
}

func (b delegationWiring) onWorkflowHumanApprovalAdvanced(ctx context.Context, run *wire.WorkflowRun) {
	if run == nil {
		return
	}
	sessionID := run.SessionID
	active, err := b.workflowMgr.Store.Runs.ActiveBySession(context.WithoutCancel(ctx), sessionID)
	if err != nil || active == nil || active.Status != wire.WorkflowRunStatusRunning {
		return
	}
	b.mgr.CancelInFlightPrompt(sessionID)
	if held, heldErr := b.workflowMgr.Obligations.HostObligationHeld(ctx, sessionID); heldErr == nil && held {
		b.coordRuntime.CoordinatorLoop().ParkForHostObligation(ctx, sessionID)
		return
	}
	b.mgr.NudgeCoordinatorLoop(ctx, sessionID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
}

func (b delegationWiring) onWorkflowFeedbackPending(ctx context.Context, sessionID, _ string) {
	b.mgr.Emit(ctx, sessionID, anchor.FeedbackPending, anchor.Envelope{})
}

func (b delegationWiring) onWorkflowToolAskOpened(ctx context.Context, sessionID, _ string) {
	b.coordRuntime.CoordinatorLoop().ParkForPendingUserInput(ctx, sessionID, "waiting for user ask")
}

func (b delegationWiring) onWorkflowFeedbackResolved(ctx context.Context, sessionID, _, _, _ string) {
	b.mgr.DropCoordinatorKick(sessionID, anchor.FeedbackPending)
	b.mgr.Emit(ctx, sessionID, anchor.FeedbackReceived, anchor.Envelope{})
	b.mgr.NudgeCoordinatorLoop(ctx, sessionID, anchor.PhaseAdvanced, anchor.FeedbackReceived, "", anchor.Envelope{})
}

func (b delegationWiring) onWorkflowReviewLoopHeld(ctx context.Context, sessionID string, decisionRequired bool) {
	id := anchor.ReviewLoopContinue
	if decisionRequired {
		id = anchor.ReviewLoopDecide
	}
	b.mgr.Emit(ctx, sessionID, id, anchor.Envelope{})
	b.mgr.NudgeCoordinatorLoop(ctx, sessionID, anchor.PhaseAdvanced, id, "", anchor.Envelope{})
}

func (b delegationWiring) onDelegationCloseout(ctx context.Context, _, sessionID, workflowRunID string) {
	if strings.TrimSpace(workflowRunID) == "" {
		return
	}
	_, _ = b.workflowMgr.Phases.TryAutoAdvance(ctx, workflowRunID)
}

// sessionVerdictCatalog is the session's effective submit_verdict schema, or
// the registered one when the session has no catalog view.
func (b delegationWiring) sessionVerdictCatalog(ctx context.Context, sessionID string) map[string]any {
	if view := b.mgr.Catalog().ViewForSessionID(ctx, sessionID); view != nil && view.ToolSchemas != nil {
		if meta, ok := view.ToolSchemas.ToolMeta("submit_verdict"); ok {
			return meta.ArgsSchema
		}
	}
	if meta, ok := b.toolRuntime.Registry.Meta("submit_verdict"); ok {
		return meta.ArgsSchema
	}
	return nil
}
