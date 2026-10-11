package workflow

import "context"

// TopologyRequestReady keeps worker dispatch behind the workflow's durable request answer.
func (m *RunManager) TopologyRequestReady(ctx context.Context, runID string) (bool, error) {
	vars, err := m.Store.GetScaffoldVars(ctx, runID)
	if err != nil {
		return false, err
	}
	state, present := requestStateFromVars(vars)
	return !present || state.Status == requestStatusResolved, nil
}
