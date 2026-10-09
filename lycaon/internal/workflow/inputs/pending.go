package inputs

import (
	"context"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"time"
)

// PendingAsk reports a pending question or approval and its start time.
// The result is valid only when err is nil.
func (m *Asks) PendingAsk(ctx context.Context, sessionID string) (time.Time, bool, error) {
	if m == nil || m.Runs == nil {
		return time.Time{}, false, nil
	}
	run, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil {
		return time.Time{}, false, err
	}
	if run == nil || run.Status != api.WorkflowRunStatusRunning {
		return time.Time{}, false, nil
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return time.Time{}, false, err
	}
	if since, ok := runstate.PendingInputSince(vars); ok {
		return since, true, nil
	}
	awaiting, err := m.Approvals.AwaitsHumanApproval(ctx, run, vars)
	if err != nil || !awaiting {
		return time.Time{}, false, err
	}
	since, _ := scaffoldvars.HumanApprovalAwaitingSince(vars)
	return since, true, nil
}

// runstate.PendingInputSince uses the same input precedence as runstate.PendingFeedbackFromVars.
