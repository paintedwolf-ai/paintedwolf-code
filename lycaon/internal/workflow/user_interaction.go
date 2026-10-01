package workflow

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/people"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// TryResolveUserFeedback applies a later chat reply to pending workflow input.
// Empty messageID still resolves. The run's StartMessageID does not. The
// reply's author answers, whichever request delivered it.
func (m *RunManager) TryResolveUserFeedback(ctx context.Context, sessionID, messageID, authorPersonID, message string) error {
	if m == nil || strings.TrimSpace(message) == "" {
		return nil
	}
	authorPersonID = strings.TrimSpace(authorPersonID)
	if authorPersonID == "" {
		return fmt.Errorf("feedback reply %q has no author", messageID)
	}
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return err
	}
	if id := strings.TrimSpace(messageID); id != "" && id == strings.TrimSpace(active.StartMessageID) {
		return nil
	}
	vars, err := m.Store.GetScaffoldVars(ctx, active.ID)
	if err != nil {
		return err
	}
	// Coordinator asks resolve only through typed endpoints.
	if _, ok := coordinatorAskPendingFromVars(vars); ok {
		return nil
	}
	if phaseID, ok := pendingFeedbackPhase(vars); ok {
		_, err = m.resolveUserFeedback(ctx, authorPersonID, sessionID, active.ID, phaseID, strings.TrimSpace(message))
		return err
	}
	key, _, ok := pendingDecisionPhase(vars)
	if !ok {
		return nil
	}
	manifest, err := m.manifestForRun(ctx, active)
	if err != nil {
		return err
	}
	def, ok := manifest.PhaseByID(active.CurrentPhase)
	if !ok || !intakeContainsKey(def, key) {
		return nil
	}
	_, err = m.resolveUserFeedback(ctx, authorPersonID, sessionID, active.ID, active.CurrentPhase, strings.TrimSpace(message))
	return err
}

// ResolveUserFeedback records the deciding person's open-ended feedback for a pending phase.
func (m *RunManager) ResolveUserFeedback(ctx context.Context, sessionID, runID, phaseID, response string) (*api.WorkflowRun, error) {
	if m == nil {
		return nil, fmt.Errorf("workflow manager not configured")
	}
	answerer, err := people.Deciding(ctx)
	if err != nil {
		return nil, err
	}
	return m.resolveUserFeedback(ctx, answerer.ID, sessionID, runID, phaseID, response)
}

func (m *RunManager) resolveUserFeedback(ctx context.Context, answererID, sessionID, runID, phaseID, response string) (*api.WorkflowRun, error) {
	response = strings.TrimSpace(response)
	if response == "" {
		return nil, ErrFeedbackEmptyResponse
	}
	run, err := m.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := verifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	if run.SessionID != sessionID {
		return nil, ErrRunNotFound
	}
	if run.Status != api.WorkflowRunStatusRunning {
		if IsTerminal(run.Status) {
			return nil, fmt.Errorf("%w: %q", ErrFeedbackNotPending, phaseID)
		}
		return nil, fmt.Errorf("%w: run %s is not running", ErrRunRevisionConflict, run.ID)
	}
	manifest, _ := m.manifestForRun(ctx, run)
	if phaseID == workflowRequestFeedbackID && manifest.Request != nil {
		return m.resolveWorkflowRequest(ctx, answererID, sessionID, run, manifest, response)
	}
	// Declared intake is keyed by question while the request uses the phase ID.
	if def, ok := manifest.PhaseByID(phaseID); ok && len(def.Intake) > 0 {
		return m.resolveIntakeFeedback(ctx, answererID, sessionID, def, run, phaseID, response)
	}
	var ask coordinatorAsk
	isToolAsk := false
	stamped, err := m.StampRunVars(ctx, runID, func(_ context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		if pending, ok := coordinatorAskPendingFromVars(vars); ok && pending.ID == phaseID {
			if err := validateCoordinatorAskForResolution(run, pending, phaseID); err != nil {
				return nil, false, err
			}
			if pending.ResponseType != workflowdef.FeedbackResponseText {
				return nil, false, fmt.Errorf("%w: %q", ErrNotChoicePhase, phaseID)
			}
			ask, isToolAsk = pending, true
			pending.State = coordinatorAskAnswered
			pending.Response = response
			pending.ResolvedBy, pending.ResolvedByPersonID = "user", answererID
			now := time.Now().UTC()
			pending.AnsweredAt = &now
			vars = setFeedbackResponse(vars, phaseID, response)
			vars = setCoordinatorAsk(vars, pending)
			ask = pending
			vars = stampHitlConsulted(vars, run.CurrentPhase)
			return vars, true, nil
		}
		if !feedbackPending(vars, phaseID) {
			return nil, false, ErrFeedbackNotPending
		}
		vars = setFeedbackResponse(vars, phaseID, response)
		return vars, true, nil
	})
	if err != nil {
		return nil, err
	}
	if stamped == nil {
		return nil, ErrRunNotFound
	}
	if isToolAsk {
		m.persistCoordinatorAskAnswer(ctx, sessionID, ask)
	}
	m.stampFeedbackAnswer(ctx, sessionID, runID, phaseID, response, answererID)
	m.notifyFeedbackResolved(ctx, sessionID, runID, phaseID, response)
	return m.TryAutoAdvance(WithExpectedRevision(ctx, stamped.Revision), runID)
}

func (m *RunManager) resolveWorkflowRequest(ctx context.Context, answererID, sessionID string, run *api.WorkflowRun, manifest workflowdef.Manifest, response string) (*api.WorkflowRun, error) {
	phaseWasActive := false
	var card *api.Message
	var resolvedVars map[string]any
	stamped, err := m.StampRunVars(ctx, run.ID, func(ctx context.Context, current *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		card = nil
		resolvedVars = nil
		if !feedbackPending(vars, workflowRequestFeedbackID) || !requestPending(vars) {
			return nil, false, ErrFeedbackNotPending
		}
		state, _ := requestStateFromVars(vars)
		sequence := 1
		if state != nil && state.Sequence > 0 {
			sequence = state.Sequence
		}
		phaseWasActive = requestPhaseActive(vars)
		vars = setFeedbackResponse(vars, workflowRequestFeedbackID, response)
		vars = setRequestState(vars, manifest.Request, requestStatusResolved, response, "answer", sequence, phaseWasActive)
		if !phaseWasActive {
			var err error
			vars, err = ApplyPhaseOnEnter(ctx, PhaseEnterRequest{
				Sessions: m.Sessions, SessionID: sessionID, Manifest: manifest, PhaseID: current.CurrentPhase,
				Vars: vars, BlueprintPath: current.BlueprintPath, Registry: m.Registry,
				ReviewSpawnFilter: m.ReviewSpawnFilter,
			})
			if err != nil {
				return nil, false, err
			}
			if def, ok := manifest.PhaseByID(current.CurrentPhase); ok {
				vars, err = ApplyAutoApproveOnApprovePhase(ctx, m, current, manifest, def, vars)
				if err != nil {
					return nil, false, err
				}
			}
			vars, card = stampPendingAnnouncement(current, vars)
		}
		resolvedVars = vars
		return vars, true, nil
	})
	if err != nil {
		return nil, err
	}
	m.notifyRequestAccepted(ctx, sessionID, response)
	m.stampFeedbackAnswer(ctx, sessionID, run.ID, workflowRequestFeedbackID, response, answererID)
	m.notifyFeedbackResolved(ctx, sessionID, run.ID, workflowRequestFeedbackID, response)
	m.appendAnnouncement(ctx, sessionID, card)
	m.notifyFeedbackPending(ctx, sessionID, resolvedVars)
	if phaseWasActive {
		m.publishSession(ctx, stamped)
		return stamped, nil
	}
	sess, err := m.Sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	activated, err := m.activateInitialWorkflowPhase(ctx, stamped, manifest, sess.WorkspacePath, true)
	if err != nil {
		return nil, err
	}
	activated, err = m.markRequestPhaseActive(ctx, activated.ID)
	if err != nil {
		return nil, err
	}
	m.publish(ctx, sess, activated)
	return activated, nil
}

// resolveIntakeFeedback refreshes intake state under its own vars stamp.
func (m *RunManager) resolveIntakeFeedback(ctx context.Context, answererID, sessionID string, def workflowdef.PhaseDef, run *api.WorkflowRun, phaseID, response string) (*api.WorkflowRun, error) {
	var key string
	var card *api.Message
	stamped, err := m.StampRunVars(ctx, run.ID, func(_ context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		card = nil
		intakeKey, ok := pendingIntakeKey(def, vars)
		if !ok {
			return nil, false, ErrFeedbackNotPending
		}
		if !decisionPending(vars, intakeKey) && !feedbackPending(vars, intakeKey) && !feedbackPending(vars, phaseID) {
			return nil, false, ErrFeedbackNotPending
		}
		key = intakeKey
		vars, err := m.applyIntakeResponse(vars, def, phaseID, response)
		if err != nil {
			return nil, false, err
		}
		vars, card = stampPendingAnnouncement(run, vars)
		return vars, true, nil
	})
	if err != nil {
		return nil, err
	}
	if stamped == nil {
		return nil, ErrRunNotFound
	}
	m.appendAnnouncement(ctx, sessionID, card)
	m.stampFeedbackAnswer(ctx, sessionID, stamped.ID, key, response, answererID)
	m.notifyFeedbackResolved(ctx, sessionID, stamped.ID, key, response)
	return m.TryAutoAdvance(WithExpectedRevision(ctx, stamped.Revision), stamped.ID)
}

// ResolveUserDecision records the deciding person's structured choice for pending input.
func (m *RunManager) ResolveUserDecision(ctx context.Context, sessionID, runID, phaseID string, choices []string, comment string) (*api.WorkflowRun, error) {
	if m == nil {
		return nil, fmt.Errorf("workflow manager not configured")
	}
	answerer, err := people.Deciding(ctx)
	if err != nil {
		return nil, err
	}
	run, err := m.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := verifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	if run.SessionID != sessionID {
		return nil, ErrRunNotFound
	}
	if run.Status != api.WorkflowRunStatusRunning {
		if IsTerminal(run.Status) {
			return nil, fmt.Errorf("%w: %q", ErrDecisionNotPending, phaseID)
		}
		return nil, fmt.Errorf("%w: run %s is not running", ErrRunRevisionConflict, run.ID)
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	def, ok := manifest.PhaseByID(phaseID)
	if !ok {
		// Declared intake cards use the intake key as PhaseID.
		if cur, ok2 := manifest.PhaseByID(run.CurrentPhase); ok2 && intakeContainsKey(cur, phaseID) {
			return m.resolveIntakeDecision(ctx, answerer.ID, sessionID, run, cur, phaseID, choices)
		}
		return m.resolveSyntheticToolDecision(ctx, answerer.ID, sessionID, runID, phaseID, choices, comment)
	}
	fb := def.OnEnter.RequestUserFeedback
	if fb == nil || !fb.ResolvedResponseType().IsChoice() {
		return nil, fmt.Errorf("%w: %q", ErrNotChoicePhase, phaseID)
	}
	cleaned := make([]string, 0, len(choices))
	for _, c := range choices {
		if c = strings.TrimSpace(c); c != "" {
			cleaned = append(cleaned, c)
		}
	}
	if len(cleaned) == 0 {
		return nil, fmt.Errorf("%w: no choice provided for %q", ErrDecisionChoiceInvalid, phaseID)
	}
	if fb.ResolvedResponseType() == workflowdef.FeedbackResponseSingleChoice && len(cleaned) != 1 {
		return nil, fmt.Errorf("%w: single_choice phase %q expects exactly one choice", ErrDecisionChoiceInvalid, phaseID)
	}
	for _, c := range cleaned {
		if !decisionOptionAllowed(fb.Options, c) && !fb.AllowOther {
			return nil, fmt.Errorf("%w: %q not in phase options", ErrDecisionChoiceInvalid, c)
		}
	}
	stamped, err := m.StampRunVars(ctx, runID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		if !decisionPending(vars, phaseID) {
			return nil, false, fmt.Errorf("%w: %q", ErrDecisionNotPending, phaseID)
		}
		if fb.ResolvedResponseType() == workflowdef.FeedbackResponseMultiChoice {
			return setDecisionChoices(vars, phaseID, cleaned, comment), true, nil
		}
		return setDecisionChoice(vars, phaseID, cleaned[0], comment), true, nil
	})
	if err != nil {
		return nil, err
	}
	if stamped == nil {
		return nil, ErrRunNotFound
	}
	answer := composeChoiceAnswer(cleaned, comment)
	m.stampFeedbackAnswer(ctx, sessionID, runID, phaseID, answer, answerer.ID)
	m.notifyFeedbackResolved(ctx, sessionID, runID, phaseID, answer)
	if len(cleaned) == 1 && isRejectChoice(cleaned[0]) {
		if ctrl := manifest.Controls.OnDecisionReject; ctrl != nil {
			if ctrl.Cancel {
				return m.Cancel(WithExpectedRevision(ctx, stamped.Revision), runID, "decision_rejected")
			}
			if ctrl.Pause {
				return m.Pause(WithExpectedRevision(ctx, stamped.Revision), runID, "decision_rejected")
			}
		}
		return stamped, nil
	}
	return m.TryAutoAdvance(WithExpectedRevision(ctx, stamped.Revision), runID)
}

// resolveSyntheticToolDecision resolves an ask that is not manifest-backed.
func (m *RunManager) resolveSyntheticToolDecision(ctx context.Context, answererID, sessionID, runID, phaseID string, choices []string, comment string) (*api.WorkflowRun, error) {
	run, err := m.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := verifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	if run.SessionID != sessionID {
		return nil, ErrRunNotFound
	}
	var ask coordinatorAsk
	var cleaned []string
	stamped, err := m.StampRunVars(ctx, runID, func(_ context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		pending, ok := coordinatorAskPendingFromVars(vars)
		if !ok || pending.ID != phaseID {
			return nil, false, fmt.Errorf("%w: %q", ErrNotChoicePhase, phaseID)
		}
		if err := validateCoordinatorAskForResolution(run, pending, phaseID); err != nil {
			return nil, false, err
		}
		if !decisionPending(vars, phaseID) {
			return nil, false, fmt.Errorf("%w: %q", ErrDecisionNotPending, phaseID)
		}
		ask = pending
		rt := pending.ResponseType
		if !rt.IsChoice() {
			return nil, false, fmt.Errorf("%w: %q", ErrNotChoicePhase, phaseID)
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
			return nil, false, fmt.Errorf("%w: no choice provided for %q", ErrDecisionChoiceInvalid, phaseID)
		}
		if rt == workflowdef.FeedbackResponseSingleChoice && len(cleaned) != 1 {
			return nil, false, fmt.Errorf("%w: single_choice phase %q expects exactly one choice", ErrDecisionChoiceInvalid, phaseID)
		}
		for _, c := range cleaned {
			if !decisionOptionAllowed(options, c) && !allowOther {
				return nil, false, fmt.Errorf("%w: %q not in phase options", ErrDecisionChoiceInvalid, c)
			}
		}
		if rt == workflowdef.FeedbackResponseMultiChoice {
			vars = setDecisionChoices(vars, phaseID, cleaned, comment)
		} else {
			vars = setDecisionChoice(vars, phaseID, cleaned[0], comment)
		}
		pending.State = coordinatorAskAnswered
		pending.Choices = append([]string(nil), cleaned...)
		pending.Response = strings.Join(cleaned, ", ")
		pending.ResolvedBy, pending.ResolvedByPersonID = "user", answererID
		now := time.Now().UTC()
		pending.AnsweredAt = &now
		vars = setCoordinatorAsk(vars, pending)
		ask = pending
		return stampHitlConsulted(vars, run.CurrentPhase), true, nil
	})
	if err != nil {
		return nil, err
	}
	if stamped == nil {
		return nil, ErrRunNotFound
	}
	choiceAnswer := strings.Join(cleaned, ", ")
	m.persistCoordinatorAskAnswer(ctx, sessionID, ask)
	m.stampFeedbackAnswer(ctx, sessionID, runID, phaseID, composeChoiceAnswer(cleaned, comment), answererID)
	m.notifyFeedbackResolved(ctx, sessionID, runID, phaseID, choiceAnswer)
	return m.TryAutoAdvance(WithExpectedRevision(ctx, stamped.Revision), runID)
}

func (m *RunManager) notifyFeedbackResolved(ctx context.Context, sessionID, runID, phaseID, response string) {
	if m == nil {
		return
	}
	if m.OnFeedbackResolved != nil {
		m.OnFeedbackResolved(ctx, sessionID, runID, phaseID, response)
	}
	if run, err := m.Store.Get(ctx, runID); err == nil && run != nil {
		m.publishSession(ctx, run)
	}
}

// stampHitlConsulted records resolved tool input for the active phase.
func stampHitlConsulted(vars map[string]any, currentPhase string) map[string]any {
	p := strings.TrimSpace(currentPhase)
	if p == "" {
		return vars
	}
	vars = cloneVars(vars)
	vars["hitl_consulted:"+p] = true
	return vars
}

// composeChoiceAnswer renders a resolved choice.
func composeChoiceAnswer(choices []string, comment string) string {
	answer := strings.Join(choices, ", ")
	if c := strings.TrimSpace(comment); c != "" {
		answer = answer + " — " + c
	}
	return answer
}

// MarkTopologyStageComplete records stage output for topology gates.
func (m *RunManager) MarkTopologyStageComplete(ctx context.Context, runID, stage, output, designForkCriterion string) error {
	if m == nil {
		return fmt.Errorf("workflow manager not configured")
	}
	stage = strings.TrimSpace(stage)
	if stage == "" {
		return fmt.Errorf("empty topology stage")
	}
	if _, err := m.StampRunVars(ctx, runID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		vars = markTopologyStage(vars, stage, output)
		if c := strings.TrimSpace(designForkCriterion); c != "" {
			vars = SetHostVar(vars, "options.criterion", c)
			vars = SetHostVar(vars, "artifact.selection.criterion", c)
		}
		return vars, true, nil
	}); err != nil {
		return err
	}
	_, _ = m.TryAutoAdvance(ctx, runID)
	_, _ = m.advanceTopologyBoundPhaseIfReady(ctx, runID, stage)
	return nil
}

// advanceTopologyBoundPhaseIfReady advances a satisfied topology phase.
func (m *RunManager) advanceTopologyBoundPhaseIfReady(ctx context.Context, runID, stage string) (*api.WorkflowRun, error) {
	run, err := m.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if IsTerminal(run.Status) || run.Status == api.WorkflowRunStatusPaused {
		return run, nil
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	def, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok {
		return run, nil
	}
	bound := strings.TrimSpace(def.BindTopologyStage) == stage
	if !bound && len(def.BindParallelGroup) > 0 {
		for _, name := range def.BindParallelGroup {
			if strings.TrimSpace(name) == stage {
				bound = true
				break
			}
		}
	}
	if !bound {
		return run, nil
	}
	vars, err := m.Store.GetScaffoldVars(ctx, runID)
	if err != nil {
		return nil, err
	}
	okGate, _, err := m.gateEvaluator().PhaseGateMet(ctx, manifest, run, vars)
	if err != nil || !okGate {
		return run, nil //nolint:nilerr // gate failure is not fatal for topology host advance hook
	}
	return m.Advance(ctx, runID)
}

func feedbackPending(vars map[string]any, phaseID string) bool {
	bucket, _ := vars["user_feedback"].(map[string]any)
	if bucket == nil {
		return false
	}
	entry, ok := bucket[phaseID].(map[string]any)
	if !ok {
		return false
	}
	pending, _ := entry["pending"].(bool)
	return pending
}

// PendingFeedbackFromVars projects the active pending-input state.
func PendingFeedbackFromVars(vars map[string]any) (api.PendingFeedback, bool) {
	if pending, ok := pendingCoordinatorAskAPI(vars); ok {
		return pending, true
	}
	if phaseID, ok := pendingFeedbackPhase(vars); ok {
		prompt, _ := feedbackPrompt(vars, phaseID)
		return api.PendingFeedback{
			PhaseID:      phaseID,
			Prompt:       prompt,
			ResponseType: string(workflowdef.FeedbackResponseText),
		}, true
	}
	if phaseID, prompt, ok := pendingDecisionPhase(vars); ok {
		out := api.PendingFeedback{
			PhaseID:      phaseID,
			Prompt:       prompt,
			ResponseType: string(workflowdef.FeedbackResponseSingleChoice),
		}
		return out, true
	}
	return api.PendingFeedback{}, false
}
func feedbackPrompt(vars map[string]any, phaseID string) (string, bool) {
	bucket, _ := vars["user_feedback"].(map[string]any)
	if bucket == nil {
		return "", false
	}
	entry, ok := bucket[phaseID].(map[string]any)
	if !ok {
		return "", false
	}
	prompt, _ := entry["prompt"].(string)
	return strings.TrimSpace(prompt), prompt != ""
}

func pendingFeedbackPhase(vars map[string]any) (string, bool) {
	bucket, _ := vars["user_feedback"].(map[string]any)
	if bucket == nil {
		return "", false
	}
	for phaseID, raw := range bucket {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if pending, _ := entry["pending"].(bool); pending {
			return phaseID, true
		}
	}
	return "", false
}

func pendingDecisionPhase(vars map[string]any) (phaseID, prompt string, ok bool) {
	bucket, _ := vars["user_decision"].(map[string]any)
	if bucket == nil {
		return "", "", false
	}
	for id, raw := range bucket {
		entry, isMap := raw.(map[string]any)
		if !isMap {
			continue
		}
		if pending, _ := entry["pending"].(bool); pending {
			p, _ := entry["prompt"].(string)
			return id, strings.TrimSpace(p), true
		}
	}
	return "", "", false
}

func decisionPending(vars map[string]any, phaseID string) bool {
	bucket, _ := vars["user_decision"].(map[string]any)
	if bucket == nil {
		return false
	}
	entry, ok := bucket[phaseID].(map[string]any)
	if !ok {
		return false
	}
	pending, _ := entry["pending"].(bool)
	return pending
}

func setFeedbackResponse(vars map[string]any, phaseID, response string) map[string]any {
	vars = cloneVars(vars)
	bucket, _ := vars["user_feedback"].(map[string]any)
	if bucket == nil {
		bucket = map[string]any{}
		vars["user_feedback"] = bucket
	}
	entry, _ := bucket[phaseID].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	entry["response"] = response
	entry["pending"] = false
	bucket[phaseID] = entry
	return vars
}

func setDecisionChoice(vars map[string]any, phaseID, choice, comment string) map[string]any {
	vars = cloneVars(vars)
	bucket, _ := vars["user_decision"].(map[string]any)
	if bucket == nil {
		bucket = map[string]any{}
		vars["user_decision"] = bucket
	}
	entry, _ := bucket[phaseID].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	entry["choice"] = choice
	entry["comment"] = strings.TrimSpace(comment)
	entry["pending"] = false
	bucket[phaseID] = entry
	return vars
}

// setDecisionChoices stores joined and individual choice forms for gate evaluation.
func setDecisionChoices(vars map[string]any, phaseID string, choices []string, comment string) map[string]any {
	vars = cloneVars(vars)
	bucket, _ := vars["user_decision"].(map[string]any)
	if bucket == nil {
		bucket = map[string]any{}
		vars["user_decision"] = bucket
	}
	entry, _ := bucket[phaseID].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	entry["choice"] = strings.Join(choices, ", ")
	entry["choices"] = append([]string(nil), choices...)
	entry["comment"] = strings.TrimSpace(comment)
	entry["pending"] = false
	bucket[phaseID] = entry
	return vars
}

func decisionOptionAllowed(options []string, choice string) bool {
	for _, o := range options {
		if strings.EqualFold(strings.TrimSpace(o), choice) {
			return true
		}
	}
	return false
}

func isRejectChoice(choice string) bool {
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "no", "reject", "cancel":
		return true
	default:
		return false
	}
}

func markTopologyStage(vars map[string]any, stage, output string) map[string]any {
	vars = cloneVars(vars)
	stages, _ := vars["topology_stages"].(map[string]any)
	if stages == nil {
		stages = map[string]any{}
		vars["topology_stages"] = stages
	}
	entry := map[string]any{"complete": true}
	output = strings.TrimSpace(output)
	if output != "" {
		entry["output"] = output
		vars = SetHostVar(vars, "topology_outputs."+stage, output)
	}
	stages[stage] = entry
	return vars
}

func (m *RunManager) applyIntakeResponse(vars map[string]any, def workflowdef.PhaseDef, phaseID, response string) (map[string]any, error) {
	key, ok := pendingIntakeKey(def, vars)
	if !ok {
		return nil, fmt.Errorf("phase %q has no pending intake key", phaseID)
	}
	catalog, err := bundledIntakeCatalog()
	if err != nil {
		return nil, err
	}
	q, ok := catalog.Get(key)
	if !ok {
		return nil, fmt.Errorf("unknown intake key %q", key)
	}
	response = strings.TrimSpace(response)
	if !slices.Contains(q.Options, response) {
		return nil, fmt.Errorf("intake response %q is not an option for key %q", response, key)
	}
	vars = setIntakeDecision(vars, key, response)
	if nextKey, more := nextPendingIntakeKey(def, vars); more {
		vars, err = latchIntakeKey(vars, nextKey)
		if err != nil {
			return nil, err
		}
	}
	return vars, nil
}

func (m *RunManager) resolveIntakeDecision(ctx context.Context, answererID, sessionID string, run *api.WorkflowRun, def workflowdef.PhaseDef, intakeKey string, choices []string) (*api.WorkflowRun, error) {
	cleaned := make([]string, 0, len(choices))
	for _, c := range choices {
		if c = strings.TrimSpace(c); c != "" {
			cleaned = append(cleaned, c)
		}
	}
	if len(cleaned) != 1 {
		return nil, fmt.Errorf("%w: intake key %q expects exactly one choice", ErrDecisionChoiceInvalid, intakeKey)
	}
	var card *api.Message
	stamped, err := m.StampRunVars(ctx, run.ID, func(_ context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		card = nil
		if !decisionPending(vars, intakeKey) {
			return nil, false, fmt.Errorf("%w: %q", ErrDecisionNotPending, intakeKey)
		}
		pending, ok := pendingIntakeKey(def, vars)
		if !ok || pending != intakeKey {
			return nil, false, fmt.Errorf("%w: %q", ErrDecisionNotPending, intakeKey)
		}
		vars, err := m.applyIntakeResponse(vars, def, run.CurrentPhase, cleaned[0])
		if err != nil {
			return nil, false, err
		}
		vars, card = stampPendingAnnouncement(run, vars)
		return vars, true, nil
	})
	if err != nil {
		return nil, err
	}
	if stamped == nil {
		return nil, ErrRunNotFound
	}
	m.appendAnnouncement(ctx, sessionID, card)
	answer := cleaned[0]
	m.stampFeedbackAnswer(ctx, sessionID, stamped.ID, intakeKey, answer, answererID)
	m.notifyFeedbackResolved(ctx, sessionID, stamped.ID, intakeKey, answer)
	return m.TryAutoAdvance(WithExpectedRevision(ctx, stamped.Revision), stamped.ID)
}

func setIntakeDecision(vars map[string]any, key, choice string) map[string]any {
	vars = SetHostVar(vars, "intake."+key, choice)
	switch key {
	case "change_size":
		vars = SetHostVar(vars, "artifact.plan.scope", choice)
	case "breaking_change":
		vars = SetHostVar(vars, "artifact.plan.breaking", choice)
	}
	return setDecisionChoice(vars, key, choice, "")
}

// SyncHumanApproval records approval evidence and advances satisfied gates.
func (m *RunManager) SyncHumanApproval(ctx context.Context, runID, projectDir string) (*api.WorkflowRun, error) {
	if m == nil || strings.TrimSpace(runID) == "" {
		return nil, nil
	}
	run, err := m.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	def, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok || def.HumanApproval == nil {
		return run, nil
	}
	return m.recordHumanApproval(ctx, runID, projectDir, run.CurrentPhase, manifest, def.HumanApproval)
}

// validateHumanApprovalReady refreshes readiness without satisfying the gate.
func (m *RunManager) validateHumanApprovalReady(ctx context.Context, runID, projectDir string) error {
	if m == nil || strings.TrimSpace(runID) == "" {
		return nil
	}
	ready := false
	bound := false
	if _, err := m.StampRunVarsInProject(ctx, runID, projectDir, func(ctx context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		ready, bound = false, false
		manifest, err := m.manifestForRun(ctx, run)
		if err != nil {
			return nil, false, err
		}
		def, ok := manifest.PhaseByID(run.CurrentPhase)
		if !ok || def.HumanApproval == nil {
			return nil, false, nil
		}
		bound = true
		vars = refreshHumanApprovalReady(ctx, m.Registry, def, vars, run.BlueprintPath, projectDir)
		ready = conditions.DotPathTruthy(vars, "human_approval.ready")
		return vars, true, nil
	}); err != nil {
		return err
	}
	if bound && !ready {
		return ErrHumanApprovalNotReady
	}
	return nil
}

// recordHumanApproval persists approval under the run-vars lock, then advances unlocked.
// It bypasses StampRunVars because a document-bound approval commits vars and the
// approval record in one transaction (CommitBlueprintApproval); like StampRunVars,
// it loads the run inside the lock so the compare-and-set carries no pre-lock revision.
func (m *RunManager) recordHumanApproval(ctx context.Context, runID, projectDir, phaseID string, manifest workflowdef.Manifest, cfg *workflowdef.HumanApprovalConfig) (*api.WorkflowRun, error) {
	if cfg == nil {
		return nil, nil
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
	vars, err := m.Store.GetScaffoldVars(ctx, runID)
	if err != nil {
		return nil, err
	}
	vars = refreshHumanApprovalReady(ctx, m.Registry, workflowdef.PhaseDef{ID: phaseID, HumanApproval: cfg}, vars, run.BlueprintPath, projectDir)
	if !conditions.DotPathTruthy(vars, "human_approval.ready") {
		// Persist refreshed readiness without satisfying the gate.
		if err := m.Store.UpdateVars(ctx, run, projectDir, vars); err != nil {
			return nil, err
		}
		return run, ErrHumanApprovalNotReady
	}
	vars = SetHumanApprovalIssued(vars, true)
	content, err := ResolveBlueprintContent(ctx, m, run, projectDir, run.BlueprintPath)
	if err != nil {
		return nil, err
	}
	approvalDigest := workflowdef.HashBlueprintContent(content)
	vars = SetHumanApprovalHash(vars, approvalDigest)
	vars = SatisfyGateInVars(vars, "human_approval")
	channel, _ := ctx.Value(approvalChannelContextKey{}).(string)
	if strings.TrimSpace(channel) == "" {
		channel = ApprovalChannelChat
	}
	if err := m.Store.CommitBlueprintApproval(ctx, run, projectDir, vars, approvalDigest, channel); err != nil {
		return nil, err
	}
	unlockVars()
	varsUnlocked = true
	return m.TryAutoAdvance(withoutExpectedRevision(ctx), runID)
}

func (m *RunManager) notifyFeedbackPending(ctx context.Context, sessionID string, vars map[string]any) {
	if m == nil || m.OnFeedbackPending == nil {
		return
	}
	if phaseID, ok := pendingFeedbackPhase(vars); ok {
		m.OnFeedbackPending(ctx, sessionID, phaseID)
		return
	}
	if phaseID, _, ok := pendingDecisionPhase(vars); ok {
		m.OnFeedbackPending(ctx, sessionID, phaseID)
	}
}

// pendingPromptFromVars rebuilds the active prompt from run variables.
func pendingPromptFromVars(vars map[string]any) (phaseID string, fb *workflowdef.UserFeedbackPrompt, ok bool) {
	if id, found := pendingFeedbackPhase(vars); found {
		prompt, _ := feedbackPrompt(vars, id)
		if strings.TrimSpace(prompt) == "" {
			return "", nil, false
		}
		return id, &workflowdef.UserFeedbackPrompt{Prompt: prompt}, true
	}
	id, prompt, found := pendingDecisionPhase(vars)
	if !found || strings.TrimSpace(prompt) == "" {
		return "", nil, false
	}
	return id, &workflowdef.UserFeedbackPrompt{
		Prompt:       prompt,
		ResponseType: workflowdef.FeedbackResponseSingleChoice,
		Options:      decisionOptionsFromVars(vars, id),
	}, true
}

func decisionOptionsFromVars(vars map[string]any, phaseID string) []string {
	bucket, _ := vars["user_decision"].(map[string]any)
	if bucket == nil {
		return nil
	}
	entry, ok := bucket[phaseID].(map[string]any)
	if !ok {
		return nil
	}
	switch raw := entry["options"].(type) {
	case []string:
		return append([]string(nil), raw...)
	case []any:
		out := make([]string, 0, len(raw))
		for _, it := range raw {
			if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	default:
		return nil
	}
}

// stampPendingAnnouncement marks the active pending input announced and returns
// the card to append once the vars commit lands. A card written before its
// marker commits would survive a failed commit and be announced twice.
func stampPendingAnnouncement(run *api.WorkflowRun, vars map[string]any) (map[string]any, *api.Message) {
	phaseID, fb, ok := pendingPromptFromVars(vars)
	if !ok {
		return vars, nil
	}
	return stampFeedbackAnnouncement(run, phaseID, fb, vars)
}

// stampFeedbackAnnouncement marks one question card announced for a phase entry.
func stampFeedbackAnnouncement(run *api.WorkflowRun, phaseID string, fb *workflowdef.UserFeedbackPrompt, vars map[string]any) (map[string]any, *api.Message) {
	if run == nil || fb == nil {
		return vars, nil
	}
	return buildFeedbackAnnouncement(run, phaseID, fb, vars, workflowAnnouncementMessageID(run.ID, phaseID))
}

// appendAnnouncement writes a stamped card after its marker committed.
func (m *RunManager) appendAnnouncement(ctx context.Context, sessionID string, msg *api.Message) {
	if m == nil || m.Sessions == nil || msg == nil {
		return
	}
	if err := m.appendSessionMessages(ctx, sessionID, *msg); err != nil {
		// The stable message id turns the immediate retry into one logical append.
		_ = m.appendSessionMessages(ctx, sessionID, *msg)
	}
}

func buildFeedbackAnnouncement(run *api.WorkflowRun, phaseID string, fb *workflowdef.UserFeedbackPrompt, vars map[string]any, messageID string) (map[string]any, *api.Message) {
	if run == nil || fb == nil {
		return vars, nil
	}
	prompt := strings.TrimSpace(fb.Prompt)
	if prompt == "" {
		return vars, nil
	}
	marker := "feedback_announced:" + phaseID
	if announced, _ := vars[marker].(bool); announced {
		return vars, nil
	}
	msg := api.Message{
		ID:            messageID,
		Role:          api.MessageRoleSystem,
		Kind:          api.MessageKindWorkflowFeedback,
		Visibility:    api.MessageVisibilityTranscript,
		Content:       prompt,
		WorkflowRunID: run.ID,
		WorkflowFeedback: &api.WorkflowFeedbackMeta{
			PhaseID:      phaseID,
			Prompt:       prompt,
			ResponseType: api.FeedbackResponseType(fb.ResolvedResponseType()),
			Options:      append([]string(nil), fb.Options...),
			AllowOther:   fb.AllowOther,
			ArtifactID:   strings.TrimSpace(fb.ArtifactID),
			ArtifactIDs:  append([]string(nil), fb.ArtifactIDs...),
			Purpose:      strings.TrimSpace(fb.Purpose),
			Secret:       apiSecretInput(fb.Secret),
		},
		CreatedAt: time.Now().UTC(),
	}
	vars = cloneVars(vars)
	vars[marker] = true
	return vars, &msg
}

// stampFeedbackAnswer makes the matching question card read-only.
func (m *RunManager) stampFeedbackAnswer(ctx context.Context, sessionID, runID, phaseID, answer, answererID string) {
	if m == nil || m.Sessions == nil || strings.TrimSpace(answer) == "" {
		return
	}
	msgs, err := m.Sessions.GetMessages(ctx, sessionID)
	if err != nil {
		return
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		meta := msgs[i].WorkflowFeedback
		if meta == nil || meta.PhaseID != phaseID || msgs[i].WorkflowRunID != runID {
			continue
		}
		if strings.TrimSpace(meta.Answer) != "" {
			return
		}
		updated := msgs[i]
		metaCopy := *meta
		metaCopy.Answer = strings.TrimSpace(answer)
		if id := strings.TrimSpace(answererID); id != "" {
			metaCopy.ResolvedBy = "user"
			metaCopy.ResolvedByPersonID = id
		}
		updated.WorkflowFeedback = &metaCopy
		if _, err := m.Sessions.UpdateMessage(ctx, sessionID, updated.ID, updated); err != nil {
			return
		}
		m.publishMessagePatch(ctx, sessionID, updated)
		return
	}
}
