package orchestration

import (
	"context"
	"fmt"
)

type restoredDelegation struct {
	ID     string
	LegIDs []string
}

// restoreWorkflowDelegation preserves the work identities of a resumed topology.
func (o *OrchestratorImpl) restoreWorkflowDelegation(ctx context.Context, runID string, keys []string, state *runState) (*restoredDelegation, error) {
	if runID == "" {
		return nil, nil
	}
	id, found, err := o.store.DelegationByWorkflowRunID(ctx, runID)
	if err != nil || !found {
		return nil, err
	}
	return o.restoreDelegation(ctx, id, keys, state)
}

func (o *OrchestratorImpl) restoreDelegation(ctx context.Context, id string, keys []string, state *runState) (*restoredDelegation, error) {
	legs, err := o.store.ListLegs(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(legs) != len(keys) {
		return nil, fmt.Errorf("stored delegation has %d legs; topology requires %d", len(legs), len(keys))
	}
	byKey := map[string]string{}
	for _, leg := range legs {
		if _, duplicate := byKey[leg.Title]; duplicate {
			return nil, fmt.Errorf("stored delegation repeats work key %q", leg.Title)
		}
		byKey[leg.Title] = leg.ID
	}
	restored := &restoredDelegation{ID: id, LegIDs: make([]string, len(keys))}
	for i, key := range keys {
		legID, ok := byKey[key]
		if !ok {
			return nil, fmt.Errorf("stored delegation lacks work key %q", key)
		}
		restored.LegIDs[i] = legID
	}
	restoreRecordedLegs(state, legs)
	return restored, nil
}
