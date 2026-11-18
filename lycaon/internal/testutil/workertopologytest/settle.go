package workertopologytest

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

// DispatchAndSettle drives a topology job through an isolated fixture queue.
func DispatchAndSettle(ctx context.Context, topology orchestration.TopologyWorkers, queue worker.WorkerQueue, batchID, unitID string, result api.WorkerResult) (*orchestration.TopologyUnit, error) {
	unit, err := topology.DispatchUnit(ctx, batchID, unitID)
	if err != nil {
		return nil, err
	}
	task, ok := queue.Get(unit.JobID)
	if !ok || task == nil {
		return nil, fmt.Errorf("dispatched job %q missing from fixture queue", unit.JobID)
	}
	claimed, err := queue.ClaimNext(ctx, worker.ClaimRequest{
		ProjectID:       task.ProjectID,
		ClaimedBy:       "topology-fixture",
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	if err != nil {
		return nil, err
	}
	if claimed == nil || claimed.ID != unit.JobID {
		return nil, fmt.Errorf("fixture queue did not claim dispatched job %q", unit.JobID)
	}
	var settled bool
	if result.Status == "failed" {
		settled, err = queue.Fail(ctx, claimed, errors.New(result.Summary))
	} else {
		settled, err = queue.Complete(ctx, claimed, result)
	}
	if err != nil {
		return nil, err
	}
	if !settled {
		return nil, fmt.Errorf("fixture queue did not settle job %q", unit.JobID)
	}
	return topology.GetUnit(ctx, batchID, unitID)
}
