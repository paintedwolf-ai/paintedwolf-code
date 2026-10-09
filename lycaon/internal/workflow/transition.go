package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/session"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// FireTransition applies an authenticated choice edge.
func (m *RunManager) FireTransition(ctx context.Context, runID, transitionID, actor string) (*api.WorkflowRun, error) {
	actor = strings.TrimSpace(actor)
	transitionID = strings.TrimSpace(transitionID)
	switch actor {
	case workflowdef.TransitionActorHuman, workflowdef.TransitionActorCoordinator:
	default:
		return nil, fmt.Errorf("%w: %q", ErrTransitionActorDenied, actor)
	}
	if transitionID == "" {
		return nil, ErrTransitionUnknown
	}
	payload := struct {
		TransitionID string `json:"transition_id"`
		Actor        string `json:"actor"`
	}{TransitionID: transitionID, Actor: actor}
	if replayed, ok, err := m.replayCommand(ctx, runID, "fire_transition", payload); err != nil || ok {
		return replayed, err
	}

	unlockVars := m.lockRunVars(runID)
	varsUnlocked := false
	defer func() {
		if !varsUnlocked {
			unlockVars()
		}
	}()
	run, err := m.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := verifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	if IsTerminal(run.Status) || run.Status != api.WorkflowRunStatusRunning {
		return nil, &NotRunnableError{RunID: runID, Status: run.Status, Reason: string(run.Status)}
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	cur, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok {
		return nil, fmt.Errorf("%w: current phase %q", ErrTransitionUnknown, run.CurrentPhase)
	}
	edge, ok := cur.TransitionByID(transitionID)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrTransitionUnknown, transitionID)
	}
	if !transitionActorAllowed(edge, actor) {
		return nil, fmt.Errorf("%w: %s on %q", ErrTransitionActorDenied, actor, transitionID)
	}

	vars, err := m.Store.GetScaffoldVars(ctx, runID)
	if err != nil {
		return nil, err
	}
	if scaffoldvars.HasPendingUserInput(vars) {
		return nil, ErrTransitionPendingInput
	}
	armed, err := m.choiceTransitionArmed(ctx, run, manifest, edge, vars)
	if err != nil {
		return nil, err
	}
	if !armed {
		return nil, fmt.Errorf("%w: %q", ErrTransitionNotArmed, transitionID)
	}
	target, ok := manifest.PhaseByID(edge.To)
	if !ok {
		return nil, fmt.Errorf("%w: target %q", ErrTransitionUnknown, edge.To)
	}

	prevPhase := run.CurrentPhase
	projectDir := m.projectDirForRun(ctx, run)

	// Target proofs start empty.
	vars = clearChoiceEntryProofs(vars, target)
	run.CurrentPhase = edge.To
	vars, err = ApplyPhaseOnEnter(ctx, PhaseEnterRequest{
		Sessions: m.Sessions, SessionID: run.SessionID, Manifest: manifest, PhaseID: edge.To,
		Vars: vars, BlueprintPath: run.BlueprintPath, Registry: m.Registry, ChoiceEntry: true,
		ReviewSpawnFilter: m.ReviewSpawnFilter,
	})
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	run.UpdatedAt = now
	terminalSink := completeTerminalPhaseEntry(run, target, now)
	var posture api.SessionPosture
	if session.ValidSessionPosture(target.OnEnter.SetPosture) {
		posture = api.SessionPosture(target.OnEnter.SetPosture)
	}
	messages := make([]api.Message, 0, 2)
	if phaseID, feedback, pending := pendingPromptFromVars(vars); pending {
		var feedbackMessage *api.Message
		vars, feedbackMessage = buildFeedbackAnnouncement(run, phaseID, feedback, vars,
			workflowCommandMessageID(run, "fire_transition", "feedback:"+phaseID))
		if feedbackMessage != nil {
			messages = append(messages, *feedbackMessage)
		}
	}
	if terminalSink {
		boundary := newCommandBoundary(run, run.Revision, "completed", run.CurrentPhase, "")
		run.EndMessageID = boundary.ID
		messages = append(messages, boundary)
	}
	if err := m.commitCommandMessages(ctx, run, "fire_transition", payload, vars, messages, posture, workflowWorkerMutation{}, nil); err != nil {
		return nil, err
	}
	m.notifyFeedbackPending(ctx, run.SessionID, vars)
	unlockVars()
	varsUnlocked = true
	if runHasBlueprint(run) {
		_ = m.syncBlueprintTranscriptForRun(ctx, run, false)
	}
	if def, ok := manifest.PhaseByID(edge.To); ok && !terminalSink {
		m.triggerPhaseEnter(ctx, run, projectDir, def)
		rc := &RunContext{
			SessionID:       run.SessionID,
			RunID:           run.ID,
			WorkflowID:      run.WorkflowID,
			WorkflowVersion: run.WorkflowVersion,
			Phase:           edge.To,
			PreviousPhase:   prevPhase,
		}
		if m.PhaseEnterHook != nil {
			m.PhaseEnterHook(ctx, rc, def)
		}
		if err := m.maybeInvokeOnPhaseEnter(ctx, run, def); err != nil {
			return nil, err
		}
	}
	m.publishPhaseAdvanced(ctx, run, prevPhase)
	if terminalSink {
		if err := m.ReconcileTerminalRun(ctx, run); err != nil {
			return nil, err
		}
	}
	return m.convergeEnteredPhases(ctx, run, manifest)
}

func transitionActorAllowed(edge workflowdef.PhaseTransitionDef, actor string) bool {
	for _, a := range edge.Actors {
		if a == actor {
			return true
		}
	}
	return false
}

func (m *RunManager) choiceTransitionArmed(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, edge workflowdef.PhaseTransitionDef, vars map[string]any) (bool, error) {
	when := strings.TrimSpace(edge.When)
	if when == "" {
		return true, nil
	}
	projectDir := ""
	if m.Sessions != nil {
		if sess, err := m.Sessions.Get(ctx, run.SessionID); err == nil && sess != nil {
			projectDir = strings.TrimSpace(sess.WorkspacePath)
		}
	}
	ec := conditions.EvalContext{
		Ctx:           ctx,
		Vars:          vars,
		Phase:         run.CurrentPhase,
		BlueprintPath: strings.TrimSpace(run.BlueprintPath),
		ProjectDir:    projectDir,
	}
	ok, err := evaluateReadinessCondition(m.Registry, ec, when)
	if err != nil {
		return false, err
	}
	return ok, nil
}

// clearChoiceEntryProofs resets evidence scoped to the target phase.
func clearChoiceEntryProofs(vars map[string]any, target workflowdef.PhaseDef) map[string]any {
	vars = cloneVars(vars)
	for _, gate := range target.Gates {
		gate = strings.TrimSpace(gate)
		if gate == "" {
			continue
		}
		vars = SetGateSatisfied(vars, gate, false)
	}
	conditions.DeleteDotPath(vars, "phase_skipped."+target.ID)
	vars = SetHostVar(vars, reviewLoopAttemptPath(target.ID), "0")
	return vars
}
