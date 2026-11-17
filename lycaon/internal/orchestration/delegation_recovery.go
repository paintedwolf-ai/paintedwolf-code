package orchestration

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

type restoredBatch struct {
	ID      string
	UnitIDs []string
}

func (o *OrchestratorImpl) restoreWorkflowBatch(ctx context.Context, runID string, keys []string, state *runState) (*restoredBatch, error) {
	if runID == "" {
		return nil, nil
	}
	id, found, err := o.workers.BatchByWorkflowRun(ctx, runID)
	if err != nil || !found {
		return nil, err
	}
	return o.restoreBatch(ctx, id, keys, state)
}

func (o *OrchestratorImpl) restoreBatch(ctx context.Context, id string, keys []string, state *runState) (*restoredBatch, error) {
	units, err := o.workers.ListUnits(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(units) != len(keys) {
		return nil, fmt.Errorf("stored topology batch has %d units; topology requires %d", len(units), len(keys))
	}
	byKey := make(map[string]string, len(units))
	for _, u := range units {
		if _, duplicate := byKey[u.WorkID]; duplicate {
			return nil, fmt.Errorf("stored topology batch repeats work key %q", u.WorkID)
		}
		byKey[u.WorkID] = u.ID
	}
	restored := &restoredBatch{ID: id, UnitIDs: make([]string, len(keys))}
	for i, key := range keys {
		unitID, ok := byKey[key]
		if !ok {
			return nil, fmt.Errorf("stored topology batch lacks work key %q", key)
		}
		restored.UnitIDs[i] = unitID
	}
	restoreRecordedUnits(state, units)
	return restored, nil
}

func restoreRecordedUnits(state *runState, units []TopologyUnit) {
	for _, u := range units {
		state.stageUnits[u.WorkID] = u.ID
		if u.Status != api.LegStatusComplete {
			continue
		}
		output := u.WorkID + " complete"
		if strings.TrimSpace(u.Summary) != "" {
			output = u.Summary
		}
		state.completed[u.WorkID] = true
		state.outputs[u.WorkID] = output
	}
}
