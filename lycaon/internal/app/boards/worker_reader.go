package boards

import (
	"context"

	"github.com/lycaon/lycaon/internal/workerlane"
	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerReader reads workers with every fact their state derives from:
// records, jobs, decisions, lane recovery and approvals held in the child.
func (r *Runtime) WorkerReader() workerops.Reader {
	lanes := workerlane.NewM14State(r.deps.Delegations.Queue)
	records := r.WorkerRecords
	checkpoints := r.deps.Sessions.Checkpoints
	toolApproval := api.CheckpointKindToolApproval
	return workerops.Reader{
		Records:   records,
		Jobs:      r.deps.Delegations.Queue,
		Decisions: r.deps.Sessions.Decisions,
		Readiness: func(ctx context.Context, actor workerrecords.Actor, workerID string) (bool, error) {
			prep, ok, err := records.LatestPreparation(ctx, actor, workerID)
			if err != nil || !ok {
				return false, err
			}
			recovery, _, err := lanes.Readiness(ctx, prep.PreparationRef)
			if err != nil || recovery {
				return recovery, err
			}
			rounds, err := records.Rounds(ctx, actor, workerID)
			for i := len(rounds) - 1; err == nil && i >= 0; i-- {
				if rounds[i].SettledAt != "" && rounds[i].ResultID != "" {
					return lanes.PromotionUnsettled(ctx, rounds[i].JobID)
				}
			}
			return false, err
		},
		ApprovalWait: func(ctx context.Context, childSessionID string) bool {
			pending, err := checkpoints.ListPending(ctx, childSessionID, &toolApproval)
			return err == nil && len(pending) > 0
		},
	}
}
