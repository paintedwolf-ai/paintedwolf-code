package delegation

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/pkg/api"
)

// DependencyDispatchGate waits for declared upstream legs.
type DependencyDispatchGate struct {
	Inner   DispatchGate
	Store   Store
	Workers interface {
		Get(string) (*api.WorkerTask, bool)
	}
}

func (g DependencyDispatchGate) Check(ctx context.Context, delegationID, legID string) (bool, string, error) {
	if g.Store == nil {
		return false, "", fmt.Errorf("delegation store required for dependency admission")
	}
	legs, err := g.Store.ListLegs(ctx, delegationID)
	if err != nil {
		return false, "", err
	}
	byID := make(map[string]api.Leg, len(legs))
	for _, leg := range legs {
		byID[leg.ID] = leg
	}
	leg, exists := byID[legID]
	if !exists {
		return false, "", ErrLegNotFound
	}
	if ready, reason := DependenciesComplete(leg, byID); !ready {
		return false, reason, nil
	}
	for _, id := range leg.DependsOn {
		upstream := byID[id]
		if upstream.WorkerID == "" {
			return false, "waiting for dependency worker " + id, nil
		}
		if g.Workers == nil {
			return false, "", fmt.Errorf("worker source required for dependency admission")
		}
		job, ok := g.Workers.Get(upstream.WorkerID)
		if !ok || !api.WorkerOutputReady(job) {
			return false, "waiting for worker output " + upstream.WorkerID, nil
		}
	}
	if g.Inner == nil {
		return true, "", nil
	}
	return g.Inner.Check(ctx, delegationID, legID)
}
