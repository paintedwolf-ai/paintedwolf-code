package runstate

import (
	"strings"
	"time"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func StampPendingAnnouncement(run *api.WorkflowRun, vars map[string]any) (map[string]any, *api.Message) {
	phaseID, fb, ok := PendingPromptFromVars(vars)
	if !ok {
		return vars, nil
	}
	return StampFeedbackAnnouncement(run, phaseID, fb, vars)
}

func StampFeedbackAnnouncement(run *api.WorkflowRun, phaseID string, fb *workflowdef.UserFeedbackPrompt, vars map[string]any) (map[string]any, *api.Message) {
	if run == nil || fb == nil {
		return vars, nil
	}
	return BuildFeedbackAnnouncement(run, phaseID, fb, vars, AnnouncementMessageID(run.ID, phaseID))
}

func PendingInputSince(vars map[string]any) (time.Time, bool) {
	if ask, ok := CoordinatorAskPendingFromVars(vars); ok {
		return ask.CreatedAt, true
	}
	if phaseID, ok := PendingFeedbackPhase(vars); ok {
		return RequestedAt(vars, "user_feedback", phaseID), true
	}
	if phaseID, _, ok := PendingDecisionPhase(vars); ok {
		return RequestedAt(vars, "user_decision", phaseID), true
	}
	return time.Time{}, false
}

func RequestedAt(vars map[string]any, bucketKey, phaseID string) time.Time {
	bucket, _ := vars[bucketKey].(map[string]any)
	entry, _ := bucket[phaseID].(map[string]any)
	raw, _ := entry["requested_at"].(string)
	at, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	return at
}
