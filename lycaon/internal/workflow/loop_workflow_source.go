package workflow

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

// ActiveRun implements loopwake.LoopWorkflowSource.
func (m *RunManager) ActiveRun(ctx context.Context, sessionID string) (*api.WorkflowRun, error) {
	return m.GetActive(ctx, sessionID)
}

// ScaffoldVars returns scaffold vars for a workflow run.
func (m *RunManager) ScaffoldVars(ctx context.Context, runID string) (map[string]any, error) {
	if m == nil || m.Store == nil {
		return nil, nil
	}
	return m.Store.GetScaffoldVars(ctx, runID)
}

// HumanApprovalAwaiting reports whether the active run is parked for the review bar.
func (m *RunManager) HumanApprovalAwaiting(ctx context.Context, sessionID string) (bool, error) {
	if m == nil || m.Store == nil {
		return false, nil
	}
	run, err := m.GetActive(ctx, sessionID)
	if err != nil || run == nil {
		return false, err
	}
	vars, err := m.Store.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return false, err
	}
	return m.runAwaitsHumanApproval(ctx, run, vars)
}
