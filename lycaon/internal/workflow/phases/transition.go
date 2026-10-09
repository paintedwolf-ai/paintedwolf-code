package phases

import (
	"context"
	"fmt"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// FireTransition applies an authenticated choice edge.
func (m *Service) FireTransition(ctx context.Context, runID, transitionID, actor string) (*api.WorkflowRun, error) {
	actor = strings.TrimSpace(actor)
	transitionID = strings.TrimSpace(transitionID)
	switch actor {
	case workflowdef.TransitionActorHuman, workflowdef.TransitionActorCoordinator:
	default:
		return nil, fmt.Errorf("%w: %q", runstate.ErrTransitionActorDenied, actor)
	}
	if transitionID == "" {
		return nil, runstate.ErrTransitionUnknown
	}
	payload := struct {
		TransitionID string `json:"transition_id"`
		Actor        string `json:"actor"`
	}{TransitionID: transitionID, Actor: actor}
	if replayed, ok, err := m.Journal.Replay(ctx, runID, "fire_transition", payload); err != nil || ok {
		return replayed, err
	}

	unlockVars := m.Vars.Lock(runID)
	varsUnlocked := false
	defer func() {
		if !varsUnlocked {
			unlockVars()
		}
	}()
	run, err := m.Runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := runstate.VerifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	if runstate.IsTerminal(run.Status) || run.Status != api.WorkflowRunStatusRunning {
		return nil, &runstate.NotRunnableError{RunID: runID, Status: run.Status, Reason: string(run.Status)}
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	cur, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok {
		return nil, fmt.Errorf("%w: current phase %q", runstate.ErrTransitionUnknown, run.CurrentPhase)
	}
	edge, ok := cur.TransitionByID(transitionID)
	if !ok {
		return nil, fmt.Errorf("%w: %q", runstate.ErrTransitionUnknown, transitionID)
	}
	if !workflowdef.TransitionActorAllowed(edge, actor) {
		return nil, fmt.Errorf("%w: %s on %q", runstate.ErrTransitionActorDenied, actor, transitionID)
	}

	vars, err := m.Runs.GetScaffoldVars(ctx, runID)
	if err != nil {
		return nil, err
	}
	if scaffoldvars.HasPendingUserInput(vars) {
		return nil, runstate.ErrTransitionPendingInput
	}
	armed, err := m.ChoiceTransitionArmed(ctx, run, manifest, edge, vars)
	if err != nil {
		return nil, err
	}
	if !armed {
		return nil, fmt.Errorf("%w: %q", runstate.ErrTransitionNotArmed, transitionID)
	}
	target, ok := manifest.PhaseByID(edge.To)
	if !ok {
		return nil, fmt.Errorf("%w: target %q", runstate.ErrTransitionUnknown, edge.To)
	}

	prevPhase := run.CurrentPhase
	projectDir := m.Resolver.ProjectDirForRun(ctx, run)

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
	terminalSink := runstate.CompleteTerminalPhaseEntry(run, target, now)
	var posture api.SessionPosture
	if sessionposture.ValidSessionPosture(target.OnEnter.SetPosture) {
		posture = api.SessionPosture(target.OnEnter.SetPosture)
	}
	messages := make([]api.Message, 0, 2)
	if phaseID, feedback, pending := runstate.PendingPromptFromVars(vars); pending {
		var feedbackMessage *api.Message
		vars, feedbackMessage = runstate.BuildFeedbackAnnouncement(run, phaseID, feedback, vars,
			runstate.CommandMessageID(run, "fire_transition", "feedback:"+phaseID))
		if feedbackMessage != nil {
			messages = append(messages, *feedbackMessage)
		}
	}
	if terminalSink {
		boundary := runstate.NewCommandBoundary(run, run.Revision, "completed", run.CurrentPhase, "")
		run.EndMessageID = boundary.ID
		messages = append(messages, boundary)
	}
	if err := m.Journal.CommitMessages(ctx, run, "fire_transition", payload, vars, messages, posture, runstate.WorkerMutation{}, nil); err != nil {
		return nil, err
	}
	m.Feedback.NotifyPending(ctx, run.SessionID, vars)
	unlockVars()
	varsUnlocked = true
	if runstate.RunHasBlueprint(run) {
		_ = m.Plans.SyncTranscriptForRun(ctx, run, false)
	}
	if def, ok := manifest.PhaseByID(edge.To); ok && !terminalSink {
		m.Entries.Trigger(ctx, run, projectDir, def)
		rc := &RunContext{
			SessionID:     run.SessionID,
			RunID:         run.ID,
			WorkflowID:    run.WorkflowID,
			Phase:         edge.To,
			PreviousPhase: prevPhase,
		}
		if m.PhaseEnterHook != nil {
			m.PhaseEnterHook(ctx, rc, def)
		}
		if err := m.Settlement.InvokeOnPhaseEnter(ctx, run, def); err != nil {
			return nil, err
		}
	}
	m.Publication.PublishPhaseAdvanced(ctx, run, prevPhase)
	if terminalSink {
		if err := m.Settlement.ReconcileTerminalRun(ctx, run); err != nil {
			return nil, err
		}
	}
	return m.convergeEnteredPhases(ctx, run, manifest)
}

func (m *Service) ChoiceTransitionArmed(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, edge workflowdef.PhaseTransitionDef, vars map[string]any) (bool, error) {
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
	vars = runstate.CloneVars(vars)
	for _, gate := range target.Gates {
		gate = strings.TrimSpace(gate)
		if gate == "" {
			continue
		}
		vars = runstate.SetGateSatisfied(vars, gate, false)
	}
	conditions.DeleteDotPath(vars, "phase_skipped."+target.ID)
	vars = runstate.SetHostVar(vars, runstate.ReviewLoopAttemptPath(target.ID), "0")
	return vars
}
