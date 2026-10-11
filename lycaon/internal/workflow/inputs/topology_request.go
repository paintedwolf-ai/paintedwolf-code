package inputs

import (
	"context"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

// TopologyRequestReady keeps worker dispatch behind the workflow's durable request answer.
func (m *Requests) TopologyRequestReady(ctx context.Context, runID string) (bool, error) {
	vars, err := m.Runs.GetScaffoldVars(ctx, runID)
	if err != nil {
		return false, err
	}
	state, present := runstate.RequestStateFromVars(vars)
	return !present || state.Status == runstate.RequestStatusResolved, nil
}
