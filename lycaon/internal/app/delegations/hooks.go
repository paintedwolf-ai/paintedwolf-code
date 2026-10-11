package delegations

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/reenter"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// OnWorkflowPhaseEnter processes phase entry triggers, guidance, and topology hooks.
func (r *Runtime) OnWorkflowPhaseEnter(ctx context.Context, rc *workflowphases.RunContext, def workflowdef.PhaseDef) {
	if rc == nil {
		return
	}
	r.deps.Sessions.Manager.Runner.Closeouts.BeginWorkflowPhase(ctx, rc.SessionID)
	// Bind progress before checklist writes on this phase.
	if r.deps.Progress != nil {
		if pStore := r.deps.Progress(); pStore != nil && progress.AdoptActiveRun(ctx, pStore, rc.SessionID, rc.RunID, "") {
			progress.NotifyWriteObservers(ctx, progress.WriteEvent{SessionID: rc.SessionID})
		}
	}
	// Evaluate topology gates against the entering phase.
	if !rc.IsRunStart() && (strings.TrimSpace(def.BindTopologyStage) != "" || len(def.BindParallelGroup) > 0) {
		if run, err := r.deps.Workflows.Manager.Store.Runs.Get(ctx, rc.RunID); err == nil && run != nil {
			run.CurrentPhase = rc.Phase
			if r.deps.StartOrchestratedTopology != nil {
				r.deps.StartOrchestratedTopology(ctx, rc.SessionID, run)
			}
		}
	}
	// Resolve phase-entry guidance through workflow bindings.
	env := anchor.Envelope{}
	if vars, err := r.deps.Workflows.Manager.Policy.ScaffoldVarsForSession(ctx, rc.SessionID); err == nil {
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
		if manifest, err := r.deps.Workflows.Manager.Resolver.ForRunID(ctx, rc.RunID); err == nil {
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
	r.deps.Sessions.Manager.Coordinator.Guidance.EmitMatch(ctx, rc.SessionID, anchor.PhaseEntered, env, anchor.RunMatch(rc, "phase", rc.Phase))
	// Host-held phases park the coordinator.
	heldByHost := false
	if def.MayHostHold() {
		held, err := r.deps.Workflows.Manager.Obligations.HostObligationHeld(ctx, rc.SessionID)
		heldByHost = err == nil && held
	}
	if heldByHost {
		// Run start completes its topology hook before parking.
		if !rc.IsRunStart() {
			r.deps.Sessions.Manager.Runner.Execution.Cancel(rc.SessionID)
		}
		if r.deps.Coordinator != nil && r.deps.Coordinator() != nil {
			r.deps.Coordinator().CoordinatorLoop().Waits.ParkForHostObligation(ctx, rc.SessionID)
		}
	}
	if mode := workflowdef.ForceExecutionMode(def.OnEnter.SetExecutionMode); mode != "" {
		r.deps.Sessions.Manager.Coordinator.Runtime.PushModeTransitionCause(rc.SessionID, surface.ModeTransitionCause{
			Kind: surface.ModeTransitionCausePhaseHook,
			Mode: mode,
		})
	} else if manifest, err := r.deps.Workflows.Manager.Resolver.ForRunID(ctx, rc.RunID); err == nil {
		if vars, err := r.deps.Workflows.Manager.Policy.ScaffoldVarsForSession(ctx, rc.SessionID); err == nil {
			if mode, ok := workflowdef.WorkflowDefaultForceMode(manifest, vars); ok {
				r.deps.Sessions.Manager.Coordinator.Runtime.PushModeTransitionCause(rc.SessionID, surface.ModeTransitionCause{
					Kind: surface.ModeTransitionCauseWorkflowDefault,
					Mode: mode,
				})
			}
		}
	}
}

// OnWorkflowPhaseReenter fires kicks configured on phase re-entry.
func (r *Runtime) OnWorkflowPhaseReenter(ctx context.Context, rc *workflowphases.RunContext, def workflowdef.PhaseDef) {
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
	r.deps.Sessions.Manager.Coordinator.Guidance.Emit(ctx, rc.SessionID, id, r.deps.Sessions.Manager.Workers.Results.EnvelopeForTerminal(ctx, rc.SessionID, ""))
}

// OnWorkflowPhaseAutoAdvanced manages coordinator nudges and topology starts on auto-advance.
func (r *Runtime) OnWorkflowPhaseAutoAdvanced(ctx context.Context, sessionID, runID, previousPhase, newPhase string) {
	run, runErr := r.deps.Workflows.Manager.Store.Runs.Get(context.WithoutCancel(ctx), runID)
	if runErr != nil || run == nil || run.Status != wire.WorkflowRunStatusRunning {
		return
	}

	manifest, err := r.deps.Workflows.Manager.Resolver.ForRunID(ctx, runID)
	if err == nil {
		if _, ok := workflowphases.ReenterLegForAdvance(manifest, previousPhase, newPhase, sessionID); ok {
			reenter.NudgeOnManifestReenter(ctx, r.deps.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Nudges, sessionID, manifest, previousPhase, newPhase)
			return
		}
	}
	// Human starts enter the first phase from an empty previous phase.
	if strings.TrimSpace(previousPhase) == "" && strings.TrimSpace(newPhase) != "" {
		postStartCtx := context.WithoutCancel(ctx)
		if run, getErr := r.deps.Workflows.Manager.Store.Runs.Get(postStartCtx, runID); getErr == nil && run != nil {
			if r.deps.StartOrchestratedTopology != nil {
				r.deps.StartOrchestratedTopology(postStartCtx, sessionID, run)
			}
		}
		if held, heldErr := r.deps.Workflows.Manager.Obligations.HostObligationHeld(postStartCtx, sessionID); heldErr == nil && held {
			r.deps.Sessions.Manager.Runner.Execution.Cancel(sessionID)
			if r.deps.Coordinator != nil && r.deps.Coordinator() != nil {
				r.deps.Coordinator().CoordinatorLoop().Waits.ParkForHostObligation(postStartCtx, sessionID)
			}
			return
		}
		r.deps.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Nudges.Nudge(ctx, sessionID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
		return
	}
	// Host transitions wake the newly entered phase.
	if strings.TrimSpace(previousPhase) != strings.TrimSpace(newPhase) {
		postAdvanceCtx := context.WithoutCancel(ctx)
		if held, heldErr := r.deps.Workflows.Manager.Obligations.HostObligationHeld(postAdvanceCtx, sessionID); heldErr == nil && held {
			if r.deps.Coordinator != nil && r.deps.Coordinator() != nil {
				r.deps.Coordinator().CoordinatorLoop().Waits.ParkForHostObligation(postAdvanceCtx, sessionID)
			}
			return
		}
		r.deps.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Nudges.Nudge(postAdvanceCtx, sessionID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
		return
	}
	r.deps.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Nudges.Nudge(ctx, sessionID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
}

// OnWorkflowRunCompleted settles completed workflow runs.
func (r *Runtime) OnWorkflowRunCompleted(ctx context.Context, run *wire.WorkflowRun) {
	if run == nil || run.Status != wire.WorkflowRunStatusComplete {
		return
	}
	if err := r.deps.Sessions.Manager.Runner.Settlement.CompleteWorkflow(ctx, run.SessionID, run.ID); err != nil {
		slog.ErrorContext(ctx, "settle completed workflow", "session_id", run.SessionID, "run_id", run.ID, "error", err)
	}
}

// OnWorkflowRunResumed resumes active workflow runs.
func (r *Runtime) OnWorkflowRunResumed(ctx context.Context, run *wire.WorkflowRun) {
	active, err := r.deps.Workflows.Manager.Store.Runs.ActiveBySession(ctx, run.SessionID)
	if err != nil || active == nil || active.ID != run.ID || active.Status != wire.WorkflowRunStatusRunning {
		return
	}
	if held, heldErr := r.deps.Workflows.Manager.Obligations.HostObligationHeld(ctx, run.SessionID); heldErr == nil && held {
		if r.deps.Coordinator != nil && r.deps.Coordinator() != nil {
			r.deps.Coordinator().CoordinatorLoop().Waits.ParkForHostObligation(ctx, run.SessionID)
		}
		return
	}
	r.deps.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Nudges.Nudge(ctx, run.SessionID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
}

// OnWorkflowHumanApprovalAdvanced nudges the loop after human approval.
func (r *Runtime) OnWorkflowHumanApprovalAdvanced(ctx context.Context, run *wire.WorkflowRun) {
	if run == nil {
		return
	}
	sessionID := run.SessionID
	active, err := r.deps.Workflows.Manager.Store.Runs.ActiveBySession(context.WithoutCancel(ctx), sessionID)
	if err != nil || active == nil || active.Status != wire.WorkflowRunStatusRunning {
		return
	}
	r.deps.Sessions.Manager.Runner.Execution.Cancel(sessionID)
	if held, heldErr := r.deps.Workflows.Manager.Obligations.HostObligationHeld(ctx, sessionID); heldErr == nil && held {
		if r.deps.Coordinator != nil && r.deps.Coordinator() != nil {
			r.deps.Coordinator().CoordinatorLoop().Waits.ParkForHostObligation(ctx, sessionID)
		}
		return
	}
	r.deps.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Nudges.Nudge(ctx, sessionID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
}

// OnWorkflowFeedbackPending emits a pending feedback kick.
func (r *Runtime) OnWorkflowFeedbackPending(ctx context.Context, sessionID, _ string) {
	r.deps.Sessions.Manager.Coordinator.Guidance.Emit(ctx, sessionID, anchor.FeedbackPending, anchor.Envelope{})
}

// OnWorkflowToolAskOpened parks coordinator for user input on tool ask.
func (r *Runtime) OnWorkflowToolAskOpened(ctx context.Context, sessionID, _ string) {
	if r.deps.Coordinator != nil && r.deps.Coordinator() != nil {
		r.deps.Coordinator().CoordinatorLoop().Waits.ParkForPendingUserInput(ctx, sessionID, "waiting for user ask")
	}
}

// OnWorkflowFeedbackResolved clears pending kick and nudges coordinator with received feedback.
func (r *Runtime) OnWorkflowFeedbackResolved(ctx context.Context, sessionID, _, _, _ string) {
	r.deps.Sessions.Manager.Coordinator.Guidance.Drop(sessionID, anchor.FeedbackPending)
	r.deps.Sessions.Manager.Coordinator.Guidance.Emit(ctx, sessionID, anchor.FeedbackReceived, anchor.Envelope{})
	r.deps.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Nudges.Nudge(ctx, sessionID, anchor.PhaseAdvanced, anchor.FeedbackReceived, "", anchor.Envelope{})
}

// OnWorkflowReviewLoopHeld emits loop decision kicks and nudges coordinator.
func (r *Runtime) OnWorkflowReviewLoopHeld(ctx context.Context, sessionID string, decisionRequired bool) {
	id := anchor.ReviewLoopContinue
	if decisionRequired {
		id = anchor.ReviewLoopDecide
	}
	r.deps.Sessions.Manager.Coordinator.Guidance.Emit(ctx, sessionID, id, anchor.Envelope{})
	r.deps.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Nudges.Nudge(ctx, sessionID, anchor.PhaseAdvanced, id, "", anchor.Envelope{})
}

// OnDelegationCloseout advances workflow phases when delegations close out.
func (r *Runtime) OnDelegationCloseout(ctx context.Context, sessionID, workflowRunID string) {
	if strings.TrimSpace(workflowRunID) == "" {
		return
	}
	_, _ = r.deps.Workflows.Manager.Phases.TryAutoAdvance(ctx, workflowRunID)
}

// SessionVerdictCatalog is the session's effective submit_verdict schema, or
// the registered one when the session has no catalog view.
func (r *Runtime) SessionVerdictCatalog(ctx context.Context, sessionID string) map[string]any {
	if view := r.deps.Sessions.Manager.Catalog.ViewForSessionID(ctx, sessionID); view != nil && view.ToolSchemas != nil {
		if meta, ok := view.ToolSchemas.ToolMeta("submit_verdict"); ok {
			return meta.ArgsSchema
		}
	}
	if r.deps.Execution.Host != nil && r.deps.Execution.Host.Registry != nil {
		if meta, ok := r.deps.Execution.Host.Registry.Meta("submit_verdict"); ok {
			return meta.ArgsSchema
		}
	}
	return nil
}
