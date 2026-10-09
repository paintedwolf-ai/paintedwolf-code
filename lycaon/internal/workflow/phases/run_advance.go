package phases

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/conditions"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowgates "github.com/lycaon/lycaon/internal/workflow/gates"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"log/slog"
	"strings"
	"time"
)

// Advance moves to the next manifest phase or completes the run.
func (m *Service) Advance(ctx context.Context, runID string) (*api.WorkflowRun, error) {
	unlockVars := m.Vars.LockOnce(runID)
	defer unlockVars()
	if replayed, ok, err := m.Journal.Replay(ctx, runID, runstate.AdvanceCommandKind, struct{}{}); err != nil || ok {
		return replayed, err
	}
	ctx = runstate.WithCommand(ctx, runstate.AdvanceCommandKind, struct{}{})
	run, err := m.Runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := runstate.VerifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	if run.Status != api.WorkflowRunStatusRunning && run.Status != api.WorkflowRunStatusPaused {
		return nil, &runstate.NotRunnableError{RunID: runID, Status: run.Status, Reason: string(run.Status)}
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, runID)
	if err != nil {
		return nil, err
	}
	if runstate.RequestPending(vars) {
		return nil, runstate.ErrTransitionPendingInput
	}
	ok, result, err := m.gateEvaluator().PhaseGateMet(ctx, manifest, run, vars)
	if err != nil {
		return nil, err
	}
	if !ok {
		workflowStartLog.Info("phase gate blocked advance",
			"session_id", run.SessionID,
			"run_id", run.ID,
			"workflow_id", run.WorkflowID,
			"phase", run.CurrentPhase,
			"failed_gate", result.FailedGate,
			"failed_leaves", result.FailedLeaves,
			"blueprint_path", run.BlueprintPath,
		)
		rejection := &runstate.PhaseGateUnmetError{
			Phase:        run.CurrentPhase,
			Reason:       result.Reason,
			FailedGate:   result.FailedGate,
			FailedLeaves: append([]string(nil), result.FailedLeaves...),
		}
		vars = runstate.RecordGateFailure(vars, result.FailedLeaves)
		return nil, m.Journal.CommitRejected(ctx, run, runstate.AdvanceCommandKind, struct{}{}, vars, rejection)
	}
	advanced, err := m.advanceToNextPhase(ctx, run, manifest, vars, false, unlockVars)
	if err != nil {
		return nil, err
	}
	return m.convergeEnteredPhases(ctx, advanced, manifest)
}

// stampPhaseGatesOnLeave records satisfied gate leaves during departure.
func stampPhaseGatesOnLeave(vars map[string]any, def workflowdef.PhaseDef) map[string]any {
	for _, gate := range def.Gates {
		gate = strings.TrimSpace(gate)
		if gate == "" {
			continue
		}
		vars = runstate.SatisfyGateInVars(vars, gate)
	}
	return vars
}

func (m *Service) advanceToNextPhase(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, vars map[string]any, auto bool, releaseVars func()) (*api.WorkflowRun, error) {
	prevPhase := run.CurrentPhase
	command := runstate.CommandFromContext(ctx, "auto_advance", struct {
		Phase string `json:"phase"`
		Auto  bool   `json:"auto"`
	}{Phase: prevPhase, Auto: auto})
	vars = runstate.RecordGateFailure(vars, nil)
	projectDir := m.Resolver.ProjectDirForRun(ctx, run)
	previousDef, hasPreviousDef := manifest.PhaseForRun(run, prevPhase)
	if hasPreviousDef {
		// Persist gate satisfaction before leaving the phase.
		vars = stampPhaseGatesOnLeave(vars, previousDef)
	}
	next, ok := manifest.ResolveAdvanceTargetForRun(run, run.CurrentPhase)
	if !ok {
		now := time.Now().UTC()
		run.Status = api.WorkflowRunStatusComplete
		run.CompletedAt = &now
	} else {
		if entryOK, result, err := m.phaseEntryMet(ctx, manifest, next, run, vars); err != nil {
			return nil, err
		} else if !entryOK {
			return nil, &runstate.PhaseGateUnmetError{
				Phase:        prevPhase,
				Reason:       result.Reason,
				FailedGate:   result.FailedGate,
				FailedLeaves: append([]string(nil), result.FailedLeaves...),
			}
		}
		run.CurrentPhase = next
		if auto && prevPhase != run.CurrentPhase {
			vars = stampHostAutoAdvancedFrom(vars, prevPhase)
		}
		vars, err := ApplyPhaseOnEnter(ctx, PhaseEnterRequest{
			Sessions: m.Sessions, SessionID: run.SessionID, Manifest: manifest, PhaseID: next,
			Vars: vars, BlueprintPath: run.BlueprintPath, Registry: m.Registry,
			ReviewSpawnFilter: m.ReviewSpawnFilter,
		})
		if err != nil {
			return nil, err
		}
		if def, ok := manifest.PhaseByID(next); ok {
			vars, err = m.Approvals.AutoApproveOnPhase(ctx, run, manifest, def, vars)
			if err != nil {
				return nil, err
			}
		}
		now := time.Now().UTC()
		run.UpdatedAt = now
		terminalSink := false
		if def, found := manifest.PhaseByID(next); found {
			terminalSink = runstate.CompleteTerminalPhaseEntry(run, def, now)
		}
		var posture api.SessionPosture
		if def, found := manifest.PhaseByID(next); found && sessionposture.ValidSessionPosture(def.OnEnter.SetPosture) {
			posture = api.SessionPosture(def.OnEnter.SetPosture)
		}
		messages := make([]api.Message, 0, 2)
		if phaseID, feedback, pending := runstate.PendingPromptFromVars(vars); pending {
			var feedbackMessage *api.Message
			vars, feedbackMessage = runstate.BuildFeedbackAnnouncement(run, phaseID, feedback, vars,
				runstate.CommandMessageID(run, command.Kind, "feedback:"+phaseID))
			if feedbackMessage != nil {
				messages = append(messages, *feedbackMessage)
			}
		}
		if terminalSink {
			boundary := runstate.NewCommandBoundary(run, run.Revision, "completed", run.CurrentPhase, "")
			run.EndMessageID = boundary.ID
			messages = append(messages, boundary)
		}
		if err := m.Journal.CommitMessages(ctx, run, command.Kind, command.Payload, vars, messages, posture, runstate.WorkerMutation{}, nil); err != nil {
			return nil, err
		}
		if releaseVars != nil {
			releaseVars()
		}
		m.Feedback.NotifyPending(ctx, run.SessionID, vars)
		if runstate.RunHasBlueprint(run) {
			_ = m.Plans.SyncTranscriptForRun(ctx, run, false)
		}
		if def, found := manifest.PhaseByID(next); found && !terminalSink {
			m.Entries.Trigger(ctx, run, projectDir, def)
			rc := &RunContext{
				SessionID:     run.SessionID,
				RunID:         run.ID,
				WorkflowID:    run.WorkflowID,
				Phase:         next,
				PreviousPhase: prevPhase,
			}
			sameReenter := strings.TrimSpace(prevPhase) == strings.TrimSpace(next) && prevPhase != ""
			if sameReenter {
				if m.PhaseReenterHook != nil {
					m.PhaseReenterHook(ctx, rc, def)
				}
			} else if m.PhaseEnterHook != nil {
				m.PhaseEnterHook(ctx, rc, def)
			}
			if err := m.Settlement.InvokeOnPhaseEnter(ctx, run, def); err != nil {
				return nil, err
			}
		}
		if prevPhase != run.CurrentPhase {
			// Publish every committed phase transition.
			m.Publication.PublishPhaseAdvanced(ctx, run, prevPhase)
		} else {
			m.Publication.PublishSession(ctx, run)
			if auto {
				m.invokePhaseAutoAdvancedIfConfigured(ctx, manifest, run, prevPhase)
			}
		}
		if terminalSink {
			if err := m.Settlement.ReconcileTerminalRun(ctx, run); err != nil {
				return nil, err
			}
		}
		return run, nil
	}
	run.UpdatedAt = time.Now().UTC()
	var endBoundary *api.Message
	if run.Status == api.WorkflowRunStatusComplete {
		msg := runstate.NewCommandBoundary(run, run.Revision, "completed", run.CurrentPhase, "")
		endBoundary = &msg
		run.EndMessageID = msg.ID
	}
	if err := m.Journal.Commit(ctx, run, command.Kind, command.Payload, vars, endBoundary, "", runstate.WorkerMutation{}, nil); err != nil {
		return nil, err
	}
	if releaseVars != nil {
		releaseVars()
	}
	if runstate.RunHasBlueprint(run) {
		_ = m.Plans.SyncTranscriptForRun(ctx, run, false)
	}
	if run.Status == api.WorkflowRunStatusComplete {
		if err := m.Settlement.ReconcileTerminalRun(ctx, run); err != nil {
			return nil, err
		}
	}
	if auto {
		if prevPhase != run.CurrentPhase {
			m.Publication.PublishPhaseAdvanced(ctx, run, prevPhase)
		} else {
			m.Publication.PublishSession(ctx, run)
		}
	}
	return run, nil
}

// runstate.CompleteTerminalPhaseEntry commits terminal entry and completion together.

// invokePhaseAutoAdvancedIfConfigured reports a configured same-phase re-entry.
func (m *Service) invokePhaseAutoAdvancedIfConfigured(ctx context.Context, manifest workflowdef.Manifest, run *api.WorkflowRun, previousPhase string) {
	if m == nil || m.Publication.OnPhaseAutoAdvanced == nil || run == nil {
		return
	}
	if _, ok := ReenterLegForAdvance(manifest, previousPhase, run.CurrentPhase, run.SessionID); ok {
		m.Publication.OnPhaseAutoAdvanced(ctx, run.SessionID, run.ID, previousPhase, run.CurrentPhase)
	}
}

func (m *Service) phaseEntryMet(ctx context.Context, manifest workflowdef.Manifest, nextPhase string, run *api.WorkflowRun, vars map[string]any) (bool, workflowgates.GateCheckResult, error) {
	def, ok := manifest.PhaseByID(nextPhase)
	if !ok {
		return true, workflowgates.GateCheckResult{}, nil
	}
	ew := strings.TrimSpace(def.EntryWhen)
	if ew == "" {
		return true, workflowgates.GateCheckResult{}, nil
	}
	if rge, ok := m.Gates.(workflowgates.ExpressionEvaluator); ok {
		return rge.ExpressionMet(ctx, ew, def, run, vars)
	}
	return false, workflowgates.GateCheckResult{Reason: ew, FailedGate: ew, FailedLeaves: []string{ew}}, nil
}

// TryAutoAdvanceThroughCommittedGates advances across satisfied phases up to maxHops.
func (m *Service) TryAutoAdvanceThroughCommittedGates(ctx context.Context, runID string, maxHops int) (*api.WorkflowRun, error) {
	if m == nil {
		return nil, nil
	}
	if maxHops <= 0 {
		maxHops = 1
	}
	run, err := m.Runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	for i := 0; i < maxHops; i++ {
		prev := run.CurrentPhase
		run, err = m.tryAutoAdvanceOne(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		if run.CurrentPhase == prev {
			break
		}
	}
	return run, nil
}

// convergeEnteredPhases advances through already-settled obligation phases.
func (m *Service) convergeEnteredPhases(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest) (*api.WorkflowRun, error) {
	if m == nil || run == nil || runstate.IsTerminal(run.Status) {
		return run, nil
	}
	for range manifest.Phases {
		def, ok := manifest.PhaseByID(run.CurrentPhase)
		if !ok || !def.HasOnEnterObligations() {
			return run, nil
		}
		previousPhase := run.CurrentPhase
		var err error
		run, err = m.tryAutoAdvanceOne(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		if run.CurrentPhase == previousPhase || runstate.IsTerminal(run.Status) {
			return run, nil
		}
	}
	return run, nil
}

// TryAutoAdvance commits Advance when all complete_when leaves for the current phase are satisfied.
func (m *Service) TryAutoAdvance(ctx context.Context, runID string) (*api.WorkflowRun, error) {
	run, err := m.tryAutoAdvanceOne(ctx, runID)
	if err != nil || run == nil || runstate.IsTerminal(run.Status) {
		return run, err
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	return m.convergeEnteredPhases(ctx, run, manifest)
}

func (m *Service) tryAutoAdvanceOne(ctx context.Context, runID string) (*api.WorkflowRun, error) {
	unlockVars := m.Vars.LockOnce(runID)
	defer unlockVars()
	run, err := m.Runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Status != api.WorkflowRunStatusRunning {
		return run, nil
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, runID)
	if err != nil {
		return nil, err
	}
	ok, _, err := m.gateEvaluator().PhaseGateMet(ctx, manifest, run, vars)
	if err != nil {
		slog.WarnContext(ctx, "phase gate evaluation failed; auto-advance held",
			"run_id", run.ID, "phase", run.CurrentPhase, "err", err)
		return run, fmt.Errorf("evaluate phase gate for run %s phase %s: %w", run.ID, run.CurrentPhase, err)
	}
	if !ok {
		return run, nil
	}
	if def, ok := manifest.PhaseForRun(run, run.CurrentPhase); ok {
		if workflowdef.EffectiveAdvancePolicy(manifest, def) == workflowdef.AdvanceWhenGateMetCoordinator {
			if !conditions.DotPathTruthy(vars, "phase_skipped."+run.CurrentPhase) {
				return run, nil
			}
		}
	}
	return m.advanceToNextPhase(ctx, run, manifest, vars, true, unlockVars)
}
