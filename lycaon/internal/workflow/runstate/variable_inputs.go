package runstate

import (
	"strings"
	"time"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func PendingPromptFromVars(vars map[string]any) (phaseID string, fb *workflowdef.UserFeedbackPrompt, ok bool) {
	if id, found := PendingFeedbackPhase(vars); found {
		prompt, _ := FeedbackPrompt(vars, id)
		if strings.TrimSpace(prompt) == "" {
			return "", nil, false
		}
		return id, &workflowdef.UserFeedbackPrompt{Prompt: prompt}, true
	}
	id, prompt, found := PendingDecisionPhase(vars)
	if !found || strings.TrimSpace(prompt) == "" {
		return "", nil, false
	}
	return id, &workflowdef.UserFeedbackPrompt{
		Prompt:       prompt,
		ResponseType: workflowdef.FeedbackResponseSingleChoice,
		Options:      DecisionOptionsFromVars(vars, id),
	}, true
}

func FeedbackPrompt(vars map[string]any, phaseID string) (string, bool) {
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

func RequestPending(vars map[string]any) bool {
	state, ok := RequestStateFromVars(vars)
	return ok && state.Status == RequestStatusPending
}

func RequestStateFromVars(vars map[string]any) (*api.WorkflowRequestState, bool) {
	raw, ok := vars[WorkflowRequestFeedbackID].(map[string]any)
	if !ok {
		return nil, false
	}
	state := &api.WorkflowRequestState{}
	state.Cadence, _ = raw["cadence"].(string)
	state.Status, _ = raw["status"].(string)
	state.Text, _ = raw["text"].(string)
	state.Source, _ = raw["source"].(string)
	switch sequence := raw["sequence"].(type) {
	case int:
		state.Sequence = sequence
	case int64:
		state.Sequence = int(sequence)
	case float64:
		state.Sequence = int(sequence)
	}
	return state, strings.TrimSpace(state.Cadence) != "" && strings.TrimSpace(state.Status) != ""
}

func StampHitlConsulted(vars map[string]any, currentPhase string) map[string]any {
	p := strings.TrimSpace(currentPhase)
	if p == "" {
		return vars
	}
	vars = CloneVars(vars)
	vars["hitl_consulted:"+p] = true
	return vars
}

func SetDecisionChoice(vars map[string]any, phaseID, choice, comment string) map[string]any {
	vars = CloneVars(vars)
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

func SecretInputMeta(secret *workflowdef.SecretInputSpec) *api.SecretInputMeta {
	if secret == nil {
		return nil
	}
	return &api.SecretInputMeta{
		Name: secret.Name, Purpose: secret.Purpose, Scope: secret.Scope,
		AgentUseTTLMs: secret.AgentUseTTLSeconds * 1000,
	}
}

func PendingFeedbackPhase(vars map[string]any) (string, bool) {
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

func PendingDecisionPhase(vars map[string]any) (phaseID, prompt string, ok bool) {
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

func DecisionOptionsFromVars(vars map[string]any, phaseID string) []string {
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

func InitializeRequestVars(vars map[string]any, request *workflowdef.ManifestRequest, explicit string, activationOnly bool) map[string]any {
	if request == nil {
		return vars
	}
	vars = CloneVars(vars)
	if activationOnly {
		return SetRequestState(vars, request, RequestStatusWaiting, "", "", 0, true)
	}
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		return SetRequestState(vars, request, RequestStatusResolved, explicit, "explicit", 1, true)
	}
	if fallback := strings.TrimSpace(request.Default); fallback != "" {
		return SetRequestState(vars, request, RequestStatusResolved, fallback, "default", 1, true)
	}
	vars = SetRequestState(vars, request, RequestStatusPending, "", "", 1, false)
	return SetFeedbackPending(vars, WorkflowRequestFeedbackID, request.Question)
}

func SetFeedbackPending(vars map[string]any, phaseID, prompt string) map[string]any {
	bucket, _ := vars["user_feedback"].(map[string]any)
	if bucket == nil {
		bucket = map[string]any{}
		vars["user_feedback"] = bucket
	}
	bucket[phaseID] = map[string]any{
		"prompt":       prompt,
		"pending":      true,
		"requested_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	return vars
}

func BuildFeedbackAnnouncement(run *api.WorkflowRun, phaseID string, fb *workflowdef.UserFeedbackPrompt, vars map[string]any, messageID string) (map[string]any, *api.Message) {
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
			Secret:       SecretInputMeta(fb.Secret),
		},
		CreatedAt: time.Now().UTC(),
	}
	vars = CloneVars(vars)
	vars[marker] = true
	return vars, &msg
}

func RequestPhaseActive(vars map[string]any) bool {
	raw, _ := vars[WorkflowRequestFeedbackID].(map[string]any)
	active, _ := raw["phase_active"].(bool)
	return active
}

func SetRequestState(vars map[string]any, request *workflowdef.ManifestRequest, status, text, source string, sequence int, phaseActive bool) map[string]any {
	vars = CloneVars(vars)
	state := map[string]any{
		"cadence":      string(request.Cadence),
		"status":       status,
		"sequence":     sequence,
		"phase_active": phaseActive,
	}
	if text != "" {
		state["text"] = text
	}
	if source != "" {
		state["source"] = source
	}
	vars[WorkflowRequestFeedbackID] = state
	return vars
}

const (
	WorkflowRequestFeedbackID = "workflow_request"
	RequestStatusWaiting      = "waiting"
	RequestStatusPending      = "pending"
	RequestStatusResolved     = "resolved"
)
