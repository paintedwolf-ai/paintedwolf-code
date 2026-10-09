package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/session"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// Advance moves to the next manifest phase or completes the run.
func (m *RunManager) Advance(ctx context.Context, runID string) (*api.WorkflowRun, error) {
	unlockVars := m.lockRunVarsOnce(runID)
	defer unlockVars()
	if replayed, ok, err := m.replayCommand(ctx, runID, advanceCommandKind, struct{}{}); err != nil || ok {
		return replayed, err
	}
	ctx = withWorkflowCommand(ctx, advanceCommandKind, struct{}{})
	run, err := m.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := verifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	if run.Status != api.WorkflowRunStatusRunning && run.Status != api.WorkflowRunStatusPaused {
		return nil, &NotRunnableError{RunID: runID, Status: run.Status, Reason: string(run.Status)}
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	vars, err := m.Store.GetScaffoldVars(ctx, runID)
	if err != nil {
		return nil, err
	}
	if requestPending(vars) {
		return nil, ErrTransitionPendingInput
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
		rejection := &PhaseGateUnmetError{
			Phase:        run.CurrentPhase,
			Reason:       result.Reason,
			FailedGate:   result.FailedGate,
			FailedLeaves: append([]string(nil), result.FailedLeaves...),
		}
		vars = recordGateFailure(vars, result.FailedLeaves)
		return nil, m.commitRejectedCommand(ctx, run, advanceCommandKind, struct{}{}, vars, rejection)
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
		vars = SatisfyGateInVars(vars, gate)
	}
	return vars
}

func (m *RunManager) advanceToNextPhase(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, vars map[string]any, auto bool, releaseVars func()) (*api.WorkflowRun, error) {
	prevPhase := run.CurrentPhase
	command := workflowCommandFromContext(ctx, "auto_advance", struct {
		Phase string `json:"phase"`
		Auto  bool   `json:"auto"`
	}{Phase: prevPhase, Auto: auto})
	vars = recordGateFailure(vars, nil)
	projectDir := m.projectDirForRun(ctx, run)
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
			return nil, &PhaseGateUnmetError{
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
			vars, err = ApplyAutoApproveOnApprovePhase(ctx, m, run, manifest, def, vars)
			if err != nil {
				return nil, err
			}
		}
		now := time.Now().UTC()
		run.UpdatedAt = now
		terminalSink := false
		if def, found := manifest.PhaseByID(next); found {
			terminalSink = completeTerminalPhaseEntry(run, def, now)
		}
		var posture api.SessionPosture
		if def, found := manifest.PhaseByID(next); found && session.ValidSessionPosture(def.OnEnter.SetPosture) {
			posture = api.SessionPosture(def.OnEnter.SetPosture)
		}
		messages := make([]api.Message, 0, 2)
		if phaseID, feedback, pending := pendingPromptFromVars(vars); pending {
			var feedbackMessage *api.Message
			vars, feedbackMessage = buildFeedbackAnnouncement(run, phaseID, feedback, vars,
				workflowCommandMessageID(run, command.Kind, "feedback:"+phaseID))
			if feedbackMessage != nil {
				messages = append(messages, *feedbackMessage)
			}
		}
		if terminalSink {
			boundary := newCommandBoundary(run, run.Revision, "completed", run.CurrentPhase, "")
			run.EndMessageID = boundary.ID
			messages = append(messages, boundary)
		}
		if err := m.commitCommandMessages(ctx, run, command.Kind, command.Payload, vars, messages, posture, workflowWorkerMutation{}, nil); err != nil {
			return nil, err
		}
		if releaseVars != nil {
			releaseVars()
		}
		m.notifyFeedbackPending(ctx, run.SessionID, vars)
		if runHasBlueprint(run) {
			_ = m.syncBlueprintTranscriptForRun(ctx, run, false)
		}
		if def, found := manifest.PhaseByID(next); found && !terminalSink {
			m.triggerPhaseEnter(ctx, run, projectDir, def)
			rc := &RunContext{
				SessionID:       run.SessionID,
				RunID:           run.ID,
				WorkflowID:      run.WorkflowID,
				WorkflowVersion: run.WorkflowVersion,
				Phase:           next,
				PreviousPhase:   prevPhase,
			}
			sameReenter := strings.TrimSpace(prevPhase) == strings.TrimSpace(next) && prevPhase != ""
			if sameReenter {
				if m.PhaseReenterHook != nil {
					m.PhaseReenterHook(ctx, rc, def)
				}
			} else if m.PhaseEnterHook != nil {
				m.PhaseEnterHook(ctx, rc, def)
			}
			if err := m.maybeInvokeOnPhaseEnter(ctx, run, def); err != nil {
				return nil, err
			}
		}
		if prevPhase != run.CurrentPhase {
			// Publish every committed phase transition.
			m.publishPhaseAdvanced(ctx, run, prevPhase)
		} else {
			m.publishSession(ctx, run)
			if auto {
				m.invokePhaseAutoAdvancedIfConfigured(ctx, manifest, run, prevPhase)
			}
		}
		if terminalSink {
			if err := m.ReconcileTerminalRun(ctx, run); err != nil {
				return nil, err
			}
		}
		return run, nil
	}
	run.UpdatedAt = time.Now().UTC()
	var endBoundary *api.Message
	if run.Status == api.WorkflowRunStatusComplete {
		msg := newCommandBoundary(run, run.Revision, "completed", run.CurrentPhase, "")
		endBoundary = &msg
		run.EndMessageID = msg.ID
	}
	if err := m.commitCommand(ctx, run, command.Kind, command.Payload, vars, endBoundary, "", workflowWorkerMutation{}, nil); err != nil {
		return nil, err
	}
	if releaseVars != nil {
		releaseVars()
	}
	if runHasBlueprint(run) {
		_ = m.syncBlueprintTranscriptForRun(ctx, run, false)
	}
	if run.Status == api.WorkflowRunStatusComplete {
		if err := m.ReconcileTerminalRun(ctx, run); err != nil {
			return nil, err
		}
	}
	if auto {
		if prevPhase != run.CurrentPhase {
			m.publishPhaseAdvanced(ctx, run, prevPhase)
		} else {
			m.publishSession(ctx, run)
		}
	}
	return run, nil
}

// completeTerminalPhaseEntry commits terminal entry and completion together.
func completeTerminalPhaseEntry(run *api.WorkflowRun, def workflowdef.PhaseDef, now time.Time) bool {
	if run == nil || !def.Terminal {
		return false
	}
	run.Status = api.WorkflowRunStatusComplete
	run.CompletedAt = &now
	return true
}

// invokePhaseAutoAdvancedIfConfigured reports a configured same-phase re-entry.
func (m *RunManager) invokePhaseAutoAdvancedIfConfigured(ctx context.Context, manifest workflowdef.Manifest, run *api.WorkflowRun, previousPhase string) {
	if m == nil || m.OnPhaseAutoAdvanced == nil || run == nil {
		return
	}
	if _, ok := ReenterLegForAdvance(manifest, previousPhase, run.CurrentPhase, run.SessionID); ok {
		m.OnPhaseAutoAdvanced(ctx, run.SessionID, run.ID, previousPhase, run.CurrentPhase)
	}
}

func (m *RunManager) phaseEntryMet(ctx context.Context, manifest workflowdef.Manifest, nextPhase string, run *api.WorkflowRun, vars map[string]any) (bool, GateCheckResult, error) {
	def, ok := manifest.PhaseByID(nextPhase)
	if !ok {
		return true, GateCheckResult{}, nil
	}
	ew := strings.TrimSpace(def.EntryWhen)
	if ew == "" {
		return true, GateCheckResult{}, nil
	}
	if rge, ok := m.Gates.(RegistryGateEvaluator); ok && rge.Registry != nil {
		return rge.evaluateCompleteWhen(ctx, ew, def, run, vars)
	}
	return false, GateCheckResult{Reason: ew, FailedGate: ew, FailedLeaves: []string{ew}}, nil
}

// TryAutoAdvanceThroughCommittedGates advances across satisfied phases up to maxHops.
func (m *RunManager) TryAutoAdvanceThroughCommittedGates(ctx context.Context, runID string, maxHops int) (*api.WorkflowRun, error) {
	if m == nil {
		return nil, nil
	}
	if maxHops <= 0 {
		maxHops = 1
	}
	run, err := m.loadRun(ctx, runID)
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
func (m *RunManager) convergeEnteredPhases(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest) (*api.WorkflowRun, error) {
	if m == nil || run == nil || IsTerminal(run.Status) {
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
		if run.CurrentPhase == previousPhase || IsTerminal(run.Status) {
			return run, nil
		}
	}
	return run, nil
}

// TryAutoAdvance commits Advance when all complete_when leaves for the current phase are satisfied.
func (m *RunManager) TryAutoAdvance(ctx context.Context, runID string) (*api.WorkflowRun, error) {
	run, err := m.tryAutoAdvanceOne(ctx, runID)
	if err != nil || run == nil || IsTerminal(run.Status) {
		return run, err
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	return m.convergeEnteredPhases(ctx, run, manifest)
}

func (m *RunManager) tryAutoAdvanceOne(ctx context.Context, runID string) (*api.WorkflowRun, error) {
	unlockVars := m.lockRunVarsOnce(runID)
	defer unlockVars()
	run, err := m.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Status != api.WorkflowRunStatusRunning {
		return run, nil
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	vars, err := m.Store.GetScaffoldVars(ctx, runID)
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
