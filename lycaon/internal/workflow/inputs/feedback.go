package inputs

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/lifecycle"
	"github.com/lycaon/lycaon/internal/workflow/publication"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"time"
)

func (m *Feedback) TryResolveUserFeedback(ctx context.Context, sessionID, messageID, authorPersonID, message string) error {
	if m == nil || strings.TrimSpace(message) == "" {
		return nil
	}
	authorPersonID = strings.TrimSpace(authorPersonID)
	if authorPersonID == "" {
		return fmt.Errorf("feedback reply %q has no author", messageID)
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return err
	}
	if id := strings.TrimSpace(messageID); id != "" && id == strings.TrimSpace(active.StartMessageID) {
		return nil
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, active.ID)
	if err != nil {
		return err
	}
	// Coordinator asks resolve only through typed endpoints.
	if _, ok := runstate.CoordinatorAskPendingFromVars(vars); ok {
		return nil
	}
	if phaseID, ok := runstate.PendingFeedbackPhase(vars); ok {
		_, err = m.resolveUserFeedback(ctx, authorPersonID, sessionID, active.ID, phaseID, strings.TrimSpace(message))
		return err
	}
	key, _, ok := runstate.PendingDecisionPhase(vars)
	if !ok {
		return nil
	}
	manifest, err := m.Resolver.ForRun(ctx, active)
	if err != nil {
		return err
	}
	def, ok := manifest.PhaseByID(active.CurrentPhase)
	if !ok || !runstate.IntakeContainsKey(def, key) {
		return nil
	}
	_, err = m.resolveUserFeedback(ctx, authorPersonID, sessionID, active.ID, active.CurrentPhase, strings.TrimSpace(message))
	return err
}
func (m *Feedback) ResolveUserFeedback(ctx context.Context, sessionID, runID, phaseID, response string) (*api.WorkflowRun, error) {
	if m == nil {
		return nil, fmt.Errorf("workflow manager not configured")
	}
	answerer, err := people.Deciding(ctx)
	if err != nil {
		return nil, err
	}
	return m.resolveUserFeedback(ctx, answerer.ID, sessionID, runID, phaseID, response)
}
func (m *Feedback) resolveUserFeedback(ctx context.Context, answererID, sessionID, runID, phaseID, response string) (*api.WorkflowRun, error) {
	response = strings.TrimSpace(response)
	if response == "" {
		return nil, runstate.ErrFeedbackEmptyResponse
	}
	run, err := m.Runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := runstate.VerifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	if run.SessionID != sessionID {
		return nil, runstate.ErrNotFound
	}
	if run.Status != api.WorkflowRunStatusRunning {
		if runstate.IsTerminal(run.Status) {
			return nil, fmt.Errorf("%w: %q", runstate.ErrFeedbackNotPending, phaseID)
		}
		return nil, fmt.Errorf("%w: run %s is not running", runstate.ErrRevisionConflict, run.ID)
	}
	manifest, _ := m.Resolver.ForRun(ctx, run)
	if phaseID == runstate.WorkflowRequestFeedbackID && manifest.Request != nil {
		return m.Requests.ResolveWorkflowRequest(ctx, answererID, sessionID, run, manifest, response)
	}
	// Declared intake is keyed by question while the request uses the phase ID.
	if def, ok := manifest.PhaseByID(phaseID); ok && len(def.Intake) > 0 {
		return m.resolveIntakeFeedback(ctx, answererID, sessionID, def, run, phaseID, response)
	}
	var ask runstate.CoordinatorAsk
	isToolAsk := false
	stamped, err := m.Vars.Stamp(ctx, runID, func(_ context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		if pending, ok := runstate.CoordinatorAskPendingFromVars(vars); ok && pending.ID == phaseID {
			if err := runstate.ValidateCoordinatorAskForResolution(run, pending, phaseID); err != nil {
				return nil, false, err
			}
			if pending.ResponseType != workflowdef.FeedbackResponseText {
				return nil, false, fmt.Errorf("%w: %q", runstate.ErrNotChoicePhase, phaseID)
			}
			ask, isToolAsk = pending, true
			pending.State = runstate.CoordinatorAskAnswered
			pending.Response = response
			pending.ResolvedBy, pending.ResolvedByPersonID = "user", answererID
			now := time.Now().UTC()
			pending.AnsweredAt = &now
			vars = runstate.SetFeedbackResponse(vars, phaseID, response)
			vars = runstate.SetCoordinatorAsk(vars, pending)
			ask = pending
			vars = runstate.StampHitlConsulted(vars, run.CurrentPhase)
			return vars, true, nil
		}
		if !runstate.FeedbackPending(vars, phaseID) {
			return nil, false, runstate.ErrFeedbackNotPending
		}
		vars = runstate.SetFeedbackResponse(vars, phaseID, response)
		return vars, true, nil
	})
	if err != nil {
		return nil, err
	}
	if stamped == nil {
		return nil, runstate.ErrNotFound
	}
	if isToolAsk {
		m.Cards.PersistCoordinatorAskAnswer(ctx, sessionID, ask)
	}
	m.Cards.StampFeedbackAnswer(ctx, sessionID, runID, phaseID, response, answererID)
	m.NotifyResolved(ctx, sessionID, runID, phaseID, response)
	return m.Phases.TryAutoAdvance(runstate.WithExpectedRevision(ctx, stamped.Revision), runID)
}
func (m *Feedback) resolveIntakeFeedback(ctx context.Context, answererID, sessionID string, def workflowdef.PhaseDef, run *api.WorkflowRun, phaseID, response string) (*api.WorkflowRun, error) {
	var key string
	var card *api.Message
	stamped, err := m.Vars.Stamp(ctx, run.ID, func(_ context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		card = nil
		intakeKey, ok := runstate.PendingIntakeKey(def, vars)
		if !ok {
			return nil, false, runstate.ErrFeedbackNotPending
		}
		if !runstate.DecisionPending(vars, intakeKey) && !runstate.FeedbackPending(vars, intakeKey) && !runstate.FeedbackPending(vars, phaseID) {
			return nil, false, runstate.ErrFeedbackNotPending
		}
		key = intakeKey
		vars, err := runstate.ApplyIntakeResponse(vars, def, phaseID, response)
		if err != nil {
			return nil, false, err
		}
		vars, card = runstate.StampPendingAnnouncement(run, vars)
		return vars, true, nil
	})
	if err != nil {
		return nil, err
	}
	if stamped == nil {
		return nil, runstate.ErrNotFound
	}
	m.Cards.AppendAnnouncement(ctx, sessionID, card)
	m.Cards.StampFeedbackAnswer(ctx, sessionID, stamped.ID, key, response, answererID)
	m.NotifyResolved(ctx, sessionID, stamped.ID, key, response)
	return m.Phases.TryAutoAdvance(runstate.WithExpectedRevision(ctx, stamped.Revision), stamped.ID)
}
func (m *Feedback) ResolveUserDecision(ctx context.Context, sessionID, runID, phaseID string, choices []string, comment string) (*api.WorkflowRun, error) {
	if m == nil {
		return nil, fmt.Errorf("workflow manager not configured")
	}
	answerer, err := people.Deciding(ctx)
	if err != nil {
		return nil, err
	}
	run, err := m.Runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := runstate.VerifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	if run.SessionID != sessionID {
		return nil, runstate.ErrNotFound
	}
	if run.Status != api.WorkflowRunStatusRunning {
		if runstate.IsTerminal(run.Status) {
			return nil, fmt.Errorf("%w: %q", runstate.ErrDecisionNotPending, phaseID)
		}
		return nil, fmt.Errorf("%w: run %s is not running", runstate.ErrRevisionConflict, run.ID)
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	def, ok := manifest.PhaseByID(phaseID)
	if !ok {
		// Declared intake cards use the intake key as PhaseID.
		if cur, ok2 := manifest.PhaseByID(run.CurrentPhase); ok2 && runstate.IntakeContainsKey(cur, phaseID) {
			return m.resolveIntakeDecision(ctx, answerer.ID, sessionID, run, cur, phaseID, choices)
		}
		return m.resolveSyntheticToolDecision(ctx, answerer.ID, sessionID, runID, phaseID, choices, comment)
	}
	fb := def.OnEnter.RequestUserFeedback
	if fb == nil || !fb.ResolvedResponseType().IsChoice() {
		return nil, fmt.Errorf("%w: %q", runstate.ErrNotChoicePhase, phaseID)
	}
	cleaned := make([]string, 0, len(choices))
	for _, c := range choices {
		if c = strings.TrimSpace(c); c != "" {
			cleaned = append(cleaned, c)
		}
	}
	if len(cleaned) == 0 {
		return nil, fmt.Errorf("%w: no choice provided for %q", runstate.ErrDecisionChoiceInvalid, phaseID)
	}
	if fb.ResolvedResponseType() == workflowdef.FeedbackResponseSingleChoice && len(cleaned) != 1 {
		return nil, fmt.Errorf("%w: single_choice phase %q expects exactly one choice", runstate.ErrDecisionChoiceInvalid, phaseID)
	}
	for _, c := range cleaned {
		if !runstate.DecisionOptionAllowed(fb.Options, c) && !fb.AllowOther {
			return nil, fmt.Errorf("%w: %q not in phase options", runstate.ErrDecisionChoiceInvalid, c)
		}
	}
	stamped, err := m.Vars.Stamp(ctx, runID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		if !runstate.DecisionPending(vars, phaseID) {
			return nil, false, fmt.Errorf("%w: %q", runstate.ErrDecisionNotPending, phaseID)
		}
		if fb.ResolvedResponseType() == workflowdef.FeedbackResponseMultiChoice {
			return runstate.SetDecisionChoices(vars, phaseID, cleaned, comment), true, nil
		}
		return runstate.SetDecisionChoice(vars, phaseID, cleaned[0], comment), true, nil
	})
	if err != nil {
		return nil, err
	}
	if stamped == nil {
		return nil, runstate.ErrNotFound
	}
	answer := runstate.ComposeChoiceAnswer(cleaned, comment)
	m.Cards.StampFeedbackAnswer(ctx, sessionID, runID, phaseID, answer, answerer.ID)
	m.NotifyResolved(ctx, sessionID, runID, phaseID, answer)
	if len(cleaned) == 1 && runstate.IsRejectChoice(cleaned[0]) {
		if ctrl := manifest.Controls.OnDecisionReject; ctrl != nil {
			if ctrl.Cancel {
				return m.Controls.Cancel(runstate.WithExpectedRevision(ctx, stamped.Revision), runID, "decision_rejected")
			}
			if ctrl.Pause {
				return m.Controls.Pause(runstate.WithExpectedRevision(ctx, stamped.Revision), runID, "decision_rejected")
			}
		}
		return stamped, nil
	}
	return m.Phases.TryAutoAdvance(runstate.WithExpectedRevision(ctx, stamped.Revision), runID)
}
func (m *Feedback) resolveSyntheticToolDecision(ctx context.Context, answererID, sessionID, runID, phaseID string, choices []string, comment string) (*api.WorkflowRun, error) {
	run, err := m.Runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := runstate.VerifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	if run.SessionID != sessionID {
		return nil, runstate.ErrNotFound
	}
	var ask runstate.CoordinatorAsk
	var cleaned []string
	stamped, err := m.Vars.Stamp(ctx, runID, func(_ context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		pending, ok := runstate.CoordinatorAskPendingFromVars(vars)
		if !ok || pending.ID != phaseID {
			return nil, false, fmt.Errorf("%w: %q", runstate.ErrNotChoicePhase, phaseID)
		}
		if err := runstate.ValidateCoordinatorAskForResolution(run, pending, phaseID); err != nil {
			return nil, false, err
		}
		if !runstate.DecisionPending(vars, phaseID) {
			return nil, false, fmt.Errorf("%w: %q", runstate.ErrDecisionNotPending, phaseID)
		}
		ask = pending
		rt := pending.ResponseType
		if !rt.IsChoice() {
			return nil, false, fmt.Errorf("%w: %q", runstate.ErrNotChoicePhase, phaseID)
		}
		options := pending.Options
		allowOther := pending.AllowOther
		cleaned = make([]string, 0, len(choices))
		for _, c := range choices {
			if c = strings.TrimSpace(c); c != "" {
				cleaned = append(cleaned, c)
			}
		}
		if len(cleaned) == 0 {
			return nil, false, fmt.Errorf("%w: no choice provided for %q", runstate.ErrDecisionChoiceInvalid, phaseID)
		}
		if rt == workflowdef.FeedbackResponseSingleChoice && len(cleaned) != 1 {
			return nil, false, fmt.Errorf("%w: single_choice phase %q expects exactly one choice", runstate.ErrDecisionChoiceInvalid, phaseID)
		}
		for _, c := range cleaned {
			if !runstate.DecisionOptionAllowed(options, c) && !allowOther {
				return nil, false, fmt.Errorf("%w: %q not in phase options", runstate.ErrDecisionChoiceInvalid, c)
			}
		}
		if rt == workflowdef.FeedbackResponseMultiChoice {
			vars = runstate.SetDecisionChoices(vars, phaseID, cleaned, comment)
		} else {
			vars = runstate.SetDecisionChoice(vars, phaseID, cleaned[0], comment)
		}
		pending.State = runstate.CoordinatorAskAnswered
		pending.Choices = append([]string(nil), cleaned...)
		pending.Response = strings.Join(cleaned, ", ")
		pending.ResolvedBy, pending.ResolvedByPersonID = "user", answererID
		now := time.Now().UTC()
		pending.AnsweredAt = &now
		vars = runstate.SetCoordinatorAsk(vars, pending)
		ask = pending
		return runstate.StampHitlConsulted(vars, run.CurrentPhase), true, nil
	})
	if err != nil {
		return nil, err
	}
	if stamped == nil {
		return nil, runstate.ErrNotFound
	}
	choiceAnswer := strings.Join(cleaned, ", ")
	m.Cards.PersistCoordinatorAskAnswer(ctx, sessionID, ask)
	m.Cards.StampFeedbackAnswer(ctx, sessionID, runID, phaseID, runstate.ComposeChoiceAnswer(cleaned, comment), answererID)
	m.NotifyResolved(ctx, sessionID, runID, phaseID, choiceAnswer)
	return m.Phases.TryAutoAdvance(runstate.WithExpectedRevision(ctx, stamped.Revision), runID)
}
func (m *Feedback) NotifyResolved(ctx context.Context, sessionID, runID, phaseID, response string) {
	if m == nil {
		return
	}
	if m.OnFeedbackResolved != nil {
		m.OnFeedbackResolved(ctx, sessionID, runID, phaseID, response)
	}
	if run, err := m.Runs.Get(ctx, runID); err == nil && run != nil {
		m.Publication.PublishSession(ctx, run)
	}
}
func (m *Feedback) resolveIntakeDecision(ctx context.Context, answererID, sessionID string, run *api.WorkflowRun, def workflowdef.PhaseDef, intakeKey string, choices []string) (*api.WorkflowRun, error) {
	cleaned := make([]string, 0, len(choices))
	for _, c := range choices {
		if c = strings.TrimSpace(c); c != "" {
			cleaned = append(cleaned, c)
		}
	}
	if len(cleaned) != 1 {
		return nil, fmt.Errorf("%w: intake key %q expects exactly one choice", runstate.ErrDecisionChoiceInvalid, intakeKey)
	}
	var card *api.Message
	stamped, err := m.Vars.Stamp(ctx, run.ID, func(_ context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		card = nil
		if !runstate.DecisionPending(vars, intakeKey) {
			return nil, false, fmt.Errorf("%w: %q", runstate.ErrDecisionNotPending, intakeKey)
		}
		pending, ok := runstate.PendingIntakeKey(def, vars)
		if !ok || pending != intakeKey {
			return nil, false, fmt.Errorf("%w: %q", runstate.ErrDecisionNotPending, intakeKey)
		}
		vars, err := runstate.ApplyIntakeResponse(vars, def, run.CurrentPhase, cleaned[0])
		if err != nil {
			return nil, false, err
		}
		vars, card = runstate.StampPendingAnnouncement(run, vars)
		return vars, true, nil
	})
	if err != nil {
		return nil, err
	}
	if stamped == nil {
		return nil, runstate.ErrNotFound
	}
	m.Cards.AppendAnnouncement(ctx, sessionID, card)
	answer := cleaned[0]
	m.Cards.StampFeedbackAnswer(ctx, sessionID, stamped.ID, intakeKey, answer, answererID)
	m.NotifyResolved(ctx, sessionID, stamped.ID, intakeKey, answer)
	return m.Phases.TryAutoAdvance(runstate.WithExpectedRevision(ctx, stamped.Revision), stamped.ID)
}
func (m *Feedback) NotifyPending(ctx context.Context, sessionID string, vars map[string]any) {
	if m == nil || m.OnFeedbackPending == nil {
		return
	}
	if phaseID, ok := runstate.PendingFeedbackPhase(vars); ok {
		m.OnFeedbackPending(ctx, sessionID, phaseID)
		return
	}
	if phaseID, _, ok := runstate.PendingDecisionPhase(vars); ok {
		m.OnFeedbackPending(ctx, sessionID, phaseID)
	}
}

type Feedback struct {
	Runs               runstate.RunsRepository
	Resolver           *catalog.Resolver
	Vars               *runstate.Variables
	Phases             PhaseProgress
	Cards              *Cards
	Requests           *Requests
	Controls           *lifecycle.Commands
	Publication        *publication.Runs
	OnFeedbackPending  func(context.Context, string, string)
	OnFeedbackResolved func(context.Context, string, string, string, string)
}

func (m *Feedback) NotifyPhasePending(ctx context.Context, sessionID, phaseID string) {
	if m != nil && m.OnFeedbackPending != nil {
		m.OnFeedbackPending(ctx, sessionID, phaseID)
	}
}
