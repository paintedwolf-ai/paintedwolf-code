package worker

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/pkg/api"
)

// BeginMergeApply atomically leases a pending branch for apply.
func (q *SQLQueue) BeginMergeApply(ctx context.Context, jobID string) (string, bool, error) {
	if q == nil || q.store == nil {
		return "", false, fmt.Errorf("worker queue store not configured")
	}
	return q.store.BeginMergeApply(ctx, jobID)
}

// RenewMergeApply extends a live merge-apply lease.
func (q *SQLQueue) RenewMergeApply(ctx context.Context, jobID, claimToken string) (bool, error) {
	if q == nil || q.store == nil {
		return false, fmt.Errorf("worker queue store not configured")
	}
	return q.store.RenewMergeApply(ctx, jobID, claimToken)
}

// ReleaseMergeApply returns an applying branch to pending under its claim.
func (q *SQLQueue) ReleaseMergeApply(ctx context.Context, jobID, claimToken string) error {
	if q == nil || q.store == nil {
		return fmt.Errorf("worker queue store not configured")
	}
	if err := q.store.ReleaseMergeApply(ctx, jobID, claimToken); err != nil {
		return err
	}
	q.refreshBoardByID(ctx, jobID)
	q.NotifyRunnable()
	return nil
}

// ReclaimExpiredMergeApply takes over an orphaned applying row whose lease lapsed.
func (q *SQLQueue) ReclaimExpiredMergeApply(ctx context.Context, jobID string) (string, bool, error) {
	if q == nil || q.store == nil {
		return "", false, fmt.Errorf("worker queue store not configured")
	}
	return q.store.ReclaimExpiredMergeApply(ctx, jobID)
}

// ListPendingOverlays returns completed jobs whose overlays await promotion.
func (q *SQLQueue) ListPendingOverlays(ctx context.Context) ([]PendingOverlay, error) {
	if q == nil || q.store == nil {
		return nil, fmt.Errorf("worker queue store not configured")
	}
	return q.store.ListPendingOverlays(ctx)
}

// ListMergeApplying returns job ids whose merge status is applying.
func (q *SQLQueue) ListMergeApplying(ctx context.Context) ([]string, error) {
	if q == nil || q.store == nil {
		return nil, fmt.Errorf("worker queue store not configured")
	}
	return q.store.ListMergeApplying(ctx)
}

// SetMergeStatus records coordinator merge state for a worker branch.
func (q *SQLQueue) SetMergeStatus(ctx context.Context, jobID string, status api.WorkerMergeStatus) error {
	if q == nil || q.store == nil {
		return nil
	}
	if err := q.store.SetMergeStatus(ctx, jobID, status); err != nil {
		return err
	}
	q.refreshBoardByID(ctx, jobID)
	q.NotifyRunnable()
	return nil
}

// SetMergeStatuses records a related set of coordinator merge transitions atomically.
func (q *SQLQueue) SetMergeStatuses(ctx context.Context, updates []MergeStatusUpdate) error {
	if q == nil || q.store == nil {
		return fmt.Errorf("worker queue store not configured")
	}
	if err := q.store.SetMergeStatuses(ctx, updates); err != nil {
		return err
	}
	q.NotifyRunnable()
	for _, update := range updates {
		q.refreshBoardByID(ctx, update.JobID)
	}
	return nil
}

// CommitPromotion finalizes one landed promotion under its lease.
func (q *SQLQueue) CommitPromotion(ctx context.Context, jobID, claimToken string, commit PromotionCommit) error {
	if q == nil || q.store == nil {
		return fmt.Errorf("worker queue store not configured")
	}
	if err := q.store.CommitPromotion(ctx, jobID, claimToken, commit); err != nil {
		return err
	}
	q.refreshBoardByID(ctx, jobID)
	q.NotifyRunnable()
	for _, update := range commit.ChildStatuses {
		q.refreshBoardByID(ctx, update.JobID)
	}
	return nil
}

// ClearWorkerWorkspace clears branch metadata after merge or abort.
func (q *SQLQueue) ClearWorkerWorkspace(ctx context.Context, jobID string) error {
	if q == nil || q.store == nil {
		return nil
	}
	if err := q.store.ClearWorkerWorkspace(ctx, jobID); err != nil {
		return err
	}
	q.refreshBoardByID(ctx, jobID)
	q.NotifyRunnable()
	return nil
}
