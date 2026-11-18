package worker

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/pkg/api"
)

// PromotionStore persists leased promotion outcomes and announces committed changes.
type PromotionStore struct {
	store    *SQLStore
	changes  *jobChanges
	runnable *runnableSignal
}

// BeginMergeApply atomically leases a pending branch for apply.
func (q *PromotionStore) BeginMergeApply(ctx context.Context, jobID string) (string, bool, error) {
	if q == nil || q.store == nil {
		return "", false, fmt.Errorf("worker queue store not configured")
	}
	return q.store.BeginMergeApply(ctx, jobID)
}

// RenewMergeApply extends a live merge-apply lease.
func (q *PromotionStore) RenewMergeApply(ctx context.Context, jobID, claimToken string) (bool, error) {
	if q == nil || q.store == nil {
		return false, fmt.Errorf("worker queue store not configured")
	}
	return q.store.RenewMergeApply(ctx, jobID, claimToken)
}

// ReleaseMergeApply returns an applying branch to pending under its claim.
func (q *PromotionStore) ReleaseMergeApply(ctx context.Context, jobID, claimToken string) error {
	if q == nil || q.store == nil {
		return fmt.Errorf("worker queue store not configured")
	}
	if err := q.store.ReleaseMergeApply(ctx, jobID, claimToken); err != nil {
		return err
	}
	q.changes.byID(ctx, jobID)
	q.runnable.NotifyRunnable()
	return nil
}

// ReclaimExpiredMergeApply takes over an orphaned applying row whose lease lapsed.
func (q *PromotionStore) ReclaimExpiredMergeApply(ctx context.Context, jobID string) (string, bool, error) {
	if q == nil || q.store == nil {
		return "", false, fmt.Errorf("worker queue store not configured")
	}
	return q.store.ReclaimExpiredMergeApply(ctx, jobID)
}

// ListPendingOverlays returns completed jobs whose overlays await promotion.
func (q *PromotionStore) ListPendingOverlays(ctx context.Context) ([]PendingOverlay, error) {
	if q == nil || q.store == nil {
		return nil, fmt.Errorf("worker queue store not configured")
	}
	return q.store.ListPendingOverlays(ctx)
}

// ListMergeApplying returns job ids whose merge status is applying.
func (q *PromotionStore) ListMergeApplying(ctx context.Context) ([]string, error) {
	if q == nil || q.store == nil {
		return nil, fmt.Errorf("worker queue store not configured")
	}
	return q.store.ListMergeApplying(ctx)
}

// SetMergeStatus records coordinator merge state for a worker branch.
func (q *PromotionStore) SetMergeStatus(ctx context.Context, jobID string, status api.WorkerMergeStatus) error {
	if q == nil || q.store == nil {
		return nil
	}
	if err := q.store.SetMergeStatus(ctx, jobID, status); err != nil {
		return err
	}
	q.changes.byID(ctx, jobID)
	q.runnable.NotifyRunnable()
	return nil
}

// SetMergeStatuses records a related set of coordinator merge transitions atomically.
func (q *PromotionStore) SetMergeStatuses(ctx context.Context, updates []MergeStatusUpdate) error {
	if q == nil || q.store == nil {
		return fmt.Errorf("worker queue store not configured")
	}
	if err := q.store.SetMergeStatuses(ctx, updates); err != nil {
		return err
	}
	q.runnable.NotifyRunnable()
	for _, update := range updates {
		q.changes.byID(ctx, update.JobID)
	}
	return nil
}

// LaterRounds reports the worker's later rounds carrying this job's changes.
func (q *PromotionStore) LaterRounds(ctx context.Context, jobID string) ([]string, bool, error) {
	if q == nil || q.store == nil {
		return nil, false, fmt.Errorf("worker queue store not configured")
	}
	return q.store.LaterRounds(ctx, jobID)
}

// CommitPromotion finalizes one landed promotion under its lease.
func (q *PromotionStore) CommitPromotion(ctx context.Context, jobID, claimToken string, commit PromotionCommit) error {
	if q == nil || q.store == nil {
		return fmt.Errorf("worker queue store not configured")
	}
	if err := q.store.CommitPromotion(ctx, jobID, claimToken, commit); err != nil {
		return err
	}
	q.changes.byID(ctx, jobID)
	q.runnable.NotifyRunnable()
	for _, update := range commit.ChildStatuses {
		q.changes.byID(ctx, update.JobID)
	}
	return nil
}

// ClearWorkerWorkspace clears branch metadata after merge or abort.
func (q *PromotionStore) ClearWorkerWorkspace(ctx context.Context, jobID string) error {
	if q == nil || q.store == nil {
		return nil
	}
	if err := q.store.ClearWorkerWorkspace(ctx, jobID); err != nil {
		return err
	}
	q.changes.byID(ctx, jobID)
	q.runnable.NotifyRunnable()
	return nil
}
