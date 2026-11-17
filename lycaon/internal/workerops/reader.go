package workerops

import (
	"context"

	"github.com/lycaon/lycaon/internal/workerlane"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// PendingApprovals lists the checkpoints pending in a session.
type PendingApprovals interface {
	ListPending(ctx context.Context, sessionID string, kind *api.CheckpointKind) ([]api.CheckpointEvent, error)
}

// NewReader reads workers with every fact their state derives from: records,
// jobs, decisions, lane recovery and tool approvals pending in the child.
// approvals may be nil when no checkpoint store is configured.
func NewReader(records *workerrecords.Store, jobs Jobs, decisions Decisions, approvals PendingApprovals) Reader {
	lanes := workerlane.NewM14State(jobs)
	toolApproval := api.CheckpointKindToolApproval
	return Reader{
		Records:   records,
		Jobs:      jobs,
		Decisions: decisions,
		Readiness: func(ctx context.Context, actor workerrecords.Actor, workerID string) (bool, error) {
			prep, ok, err := records.LatestPreparation(ctx, actor, workerID)
			if err != nil || !ok {
				return false, err
			}
			recovery, _, err := lanes.Readiness(ctx, prep.PreparationRef)
			if err != nil || recovery {
				return recovery, err
			}
			round, _, hasResult, err := records.LatestSettledResult(ctx, actor, workerID)
			if err != nil || !hasResult {
				return false, err
			}
			return lanes.PromotionUnsettled(ctx, round.JobID)
		},
		ApprovalWait: func(ctx context.Context, childSessionID string) bool {
			if approvals == nil {
				return false
			}
			pending, err := approvals.ListPending(ctx, childSessionID, &toolApproval)
			return err == nil && len(pending) > 0
		},
	}
}
