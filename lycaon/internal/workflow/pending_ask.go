package workflow

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/scaffoldvars"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// PendingAsk reports a pending question or approval and its start time.
// The result is valid only when err is nil.
func (m *RunManager) PendingAsk(ctx context.Context, sessionID string) (time.Time, bool, error) {
	if m == nil || m.Store == nil {
		return time.Time{}, false, nil
	}
	run, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil {
		return time.Time{}, false, err
	}
	if run == nil || run.Status != api.WorkflowRunStatusRunning {
		return time.Time{}, false, nil
	}
	vars, err := m.Store.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return time.Time{}, false, err
	}
	if since, ok := pendingInputSince(vars); ok {
		return since, true, nil
	}
	awaiting, err := m.runAwaitsHumanApproval(ctx, run, vars)
	if err != nil || !awaiting {
		return time.Time{}, false, err
	}
	since, _ := scaffoldvars.HumanApprovalAwaitingSince(vars)
	return since, true, nil
}

// pendingInputSince uses the same input precedence as PendingFeedbackFromVars.
func pendingInputSince(vars map[string]any) (time.Time, bool) {
	if ask, ok := coordinatorAskPendingFromVars(vars); ok {
		return ask.CreatedAt, true
	}
	if phaseID, ok := pendingFeedbackPhase(vars); ok {
		return requestedAt(vars, "user_feedback", phaseID), true
	}
	if phaseID, _, ok := pendingDecisionPhase(vars); ok {
		return requestedAt(vars, "user_decision", phaseID), true
	}
	return time.Time{}, false
}

func requestedAt(vars map[string]any, bucketKey, phaseID string) time.Time {
	bucket, _ := vars[bucketKey].(map[string]any)
	entry, _ := bucket[phaseID].(map[string]any)
	raw, _ := entry["requested_at"].(string)
	at, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	return at
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
