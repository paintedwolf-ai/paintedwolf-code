package runstate

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/pkg/api"
)

// Batches serializes coordinator batch transitions against current run state.
type Batches struct {
	Runs RunsRepository
	Vars *Variables
}

// applyCoordinatorBatchEvent is the single serialized writer for coordinator_batch scaffold vars.
func (m *Batches) ApplyCoordinatorBatchEvent(ctx context.Context, sessionID string, ev batch.Event, eventSeq int) error {
	if m == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil {
		return err
	}
	if active == nil {
		return nil
	}
	// Batch events race with ask injects and resolvers touching the same blob,
	// so the transition is derived and committed inside one locked attempt.
	if _, err := m.Vars.Stamp(ctx, active.ID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		newVars, _, applied := batch.ApplyTransition(vars, ev, eventSeq)
		if !applied {
			return nil, false, nil
		}
		return newVars, true, nil
	}); err != nil {
		return err
	}
	return nil
}
