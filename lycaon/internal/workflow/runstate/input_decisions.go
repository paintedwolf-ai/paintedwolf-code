package runstate

import (
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"time"
)

func FeedbackPending(vars map[string]any, phaseID string) bool {
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

func PendingFeedbackFromVars(vars map[string]any) (api.PendingFeedback, bool) {
	if pending, ok := PendingCoordinatorAskAPI(vars); ok {
		return pending, true
	}
	if phaseID, ok := PendingFeedbackPhase(vars); ok {
		prompt, _ := FeedbackPrompt(vars, phaseID)
		return api.PendingFeedback{
			PhaseID:      phaseID,
			Prompt:       prompt,
			ResponseType: string(workflowdef.FeedbackResponseText),
		}, true
	}
	if phaseID, prompt, ok := PendingDecisionPhase(vars); ok {
		out := api.PendingFeedback{
			PhaseID:      phaseID,
			Prompt:       prompt,
			ResponseType: string(workflowdef.FeedbackResponseSingleChoice),
		}
		return out, true
	}
	return api.PendingFeedback{}, false
}

func DecisionPending(vars map[string]any, phaseID string) bool {
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

func SetFeedbackResponse(vars map[string]any, phaseID, response string) map[string]any {
	vars = CloneVars(vars)
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

func SetDecisionChoices(vars map[string]any, phaseID string, choices []string, comment string) map[string]any {
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
	entry["choice"] = strings.Join(choices, ", ")
	entry["choices"] = append([]string(nil), choices...)
	entry["comment"] = strings.TrimSpace(comment)
	entry["pending"] = false
	bucket[phaseID] = entry
	return vars
}

func DecisionOptionAllowed(options []string, choice string) bool {
	for _, o := range options {
		if strings.EqualFold(strings.TrimSpace(o), choice) {
			return true
		}
	}
	return false
}

func IsRejectChoice(choice string) bool {
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "no", "reject", "cancel":
		return true
	default:
		return false
	}
}

func ComposeChoiceAnswer(choices []string, comment string) string {
	answer := strings.Join(choices, ", ")
	if c := strings.TrimSpace(comment); c != "" {
		answer = answer + " — " + c
	}
	return answer
}

func SetDecisionPending(vars map[string]any, phaseID, prompt string, options []string) map[string]any {
	bucket, _ := vars["user_decision"].(map[string]any)
	if bucket == nil {
		bucket = map[string]any{}
		vars["user_decision"] = bucket
	}
	bucket[phaseID] = map[string]any{
		"prompt":       prompt,
		"options":      append([]string(nil), options...),
		"pending":      true,
		"requested_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	return vars
}

func CloneAskVars(vars map[string]any) map[string]any {
	out := CloneVars(vars)
	for _, key := range []string{"user_feedback", "user_decision"} {
		bucket, _ := out[key].(map[string]any)
		if bucket == nil {
			continue
		}
		copied := make(map[string]any, len(bucket))
		for phaseID, raw := range bucket {
			if entry, ok := raw.(map[string]any); ok {
				entryCopy := make(map[string]any, len(entry))
				for ek, ev := range entry {
					entryCopy[ek] = ev
				}
				copied[phaseID] = entryCopy
			} else {
				copied[phaseID] = raw
			}
		}
		out[key] = copied
	}
	return out
}
