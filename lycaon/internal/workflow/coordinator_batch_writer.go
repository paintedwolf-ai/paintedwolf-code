package workflow

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/pkg/api"
)

// applyCoordinatorBatchEvent is the single serialized writer for coordinator_batch scaffold vars.
func (m *RunManager) applyCoordinatorBatchEvent(ctx context.Context, sessionID string, ev batch.Event, eventSeq int) (batch.State, error) {
	if m == nil {
		return batch.State{}, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return batch.State{}, nil
	}
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil {
		return batch.State{}, err
	}
	if active == nil {
		return batch.State{}, nil
	}
	// Batch events race with ask injects and resolvers touching the same blob,
	// so the transition is derived and committed inside one locked attempt.
	var state batch.State
	if _, err := m.StampRunVars(ctx, active.ID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		newVars, transitioned, applied := batch.ApplyTransition(vars, ev, eventSeq)
		if !applied {
			state = batch.Read(vars)
			return nil, false, nil
		}
		state = transitioned
		return newVars, true, nil
	}); err != nil {
		return batch.State{}, err
	}
	return state, nil
}
