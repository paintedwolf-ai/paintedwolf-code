package workflow

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/scaffoldvars"
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
