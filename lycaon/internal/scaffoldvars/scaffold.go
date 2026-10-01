// Package scaffoldvars reads pending user input and approval state from workflow scaffold host vars.
package scaffoldvars

import (
	"strings"
	"time"
)

// HasPendingUserInput reports feedback or decision input awaiting the user.
func HasPendingUserInput(vars map[string]any) bool {
	_, ok := PendingFeedbackPhase(vars)
	return ok
}

// PendingFeedbackPhase returns the phase of a pending user_feedback or
// user_decision ask.
func PendingFeedbackPhase(vars map[string]any) (string, bool) {
	if phaseID, ok := pendingBucketPhase(vars, "user_feedback"); ok {
		return phaseID, true
	}
	return pendingBucketPhase(vars, "user_decision")
}

func pendingBucketPhase(vars map[string]any, bucketKey string) (string, bool) {
	bucket, _ := vars[bucketKey].(map[string]any)
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

// AnyDecisionPending reports whether any user_decision entry is pending.
func AnyDecisionPending(vars map[string]any) bool {
	_, ok := pendingBucketPhase(vars, "user_decision")
	return ok
}

// HumanApprovalAwaitingSincePath holds when the current approval wait opened.
const HumanApprovalAwaitingSincePath = "human_approval.awaiting_since"

// HumanApprovalAwaitingSince reports when the current approval wait opened.
func HumanApprovalAwaitingSince(vars map[string]any) (time.Time, bool) {
	bucket, _ := vars["human_approval"].(map[string]any)
	raw := strings.TrimSpace(varString(bucket["awaiting_since"]))
	if raw == "" {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	return at, err == nil
}

// HumanApprovalAwaiting reports whether human_approval is active and ready but
// not yet issued against a recorded blueprint hash.
func HumanApprovalAwaiting(vars map[string]any) bool {
	bucket, _ := vars["human_approval"].(map[string]any)
	if bucket == nil {
		return false
	}
	if !varTruthy(bucket["active"]) || !varTruthy(bucket["ready"]) {
		return false
	}
	if strings.TrimSpace(varString(bucket["blueprint_path"])) == "" {
		return false
	}
	if varTruthy(bucket["issued"]) {
		if strings.TrimSpace(varString(bucket["blueprint_hash"])) != "" {
			return false
		}
	}
	return true
}

func varTruthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.TrimSpace(t) != "" && strings.ToLower(t) != "false"
	default:
		return v != nil
	}
}

func varString(v any) string {
	s, _ := v.(string)
	return s
}
