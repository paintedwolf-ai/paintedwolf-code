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
func (p *PromotionStore) BeginMergeApply(ctx context.Context, jobID string) (string, bool, error) {
	if p == nil || p.store == nil {
		return "", false, fmt.Errorf("worker queue store not configured")
	}
	return p.store.BeginMergeApply(ctx, jobID)
}

// RenewMergeApply extends a live merge-apply lease.
func (p *PromotionStore) RenewMergeApply(ctx context.Context, jobID, claimToken string) (bool, error) {
	if p == nil || p.store == nil {
		return false, fmt.Errorf("worker queue store not configured")
	}
	return p.store.RenewMergeApply(ctx, jobID, claimToken)
}

// ReleaseMergeApply returns an applying branch to pending under its claim.
func (p *PromotionStore) ReleaseMergeApply(ctx context.Context, jobID, claimToken string) error {
	if p == nil || p.store == nil {
		return fmt.Errorf("worker queue store not configured")
	}
	if err := p.store.ReleaseMergeApply(ctx, jobID, claimToken); err != nil {
		return err
	}
	p.changes.byID(ctx, jobID)
	p.runnable.NotifyRunnable()
	return nil
}

// ReclaimExpiredMergeApply takes over an orphaned applying row whose lease lapsed.
func (p *PromotionStore) ReclaimExpiredMergeApply(ctx context.Context, jobID string) (string, bool, error) {
	if p == nil || p.store == nil {
		return "", false, fmt.Errorf("worker queue store not configured")
	}
	return p.store.ReclaimExpiredMergeApply(ctx, jobID)
}

// ListPendingOverlays returns completed jobs whose overlays await promotion.
func (p *PromotionStore) ListPendingOverlays(ctx context.Context) ([]PendingOverlay, error) {
	if p == nil || p.store == nil {
		return nil, fmt.Errorf("worker queue store not configured")
	}
	return p.store.ListPendingOverlays(ctx)
}

// ListMergeApplying returns job ids whose merge status is applying.
func (p *PromotionStore) ListMergeApplying(ctx context.Context) ([]string, error) {
	if p == nil || p.store == nil {
		return nil, fmt.Errorf("worker queue store not configured")
	}
	return p.store.ListMergeApplying(ctx)
}

// SetMergeStatus records coordinator merge state for a worker branch.
func (p *PromotionStore) SetMergeStatus(ctx context.Context, jobID string, status api.WorkerMergeStatus) error {
	if p == nil || p.store == nil {
		return nil
	}
	if err := p.store.SetMergeStatus(ctx, jobID, status); err != nil {
		return err
	}
	p.changes.byID(ctx, jobID)
	p.runnable.NotifyRunnable()
	return nil
}

// SetMergeStatuses records a related set of coordinator merge transitions atomically.
func (p *PromotionStore) SetMergeStatuses(ctx context.Context, updates []MergeStatusUpdate) error {
	if p == nil || p.store == nil {
		return fmt.Errorf("worker queue store not configured")
	}
	if err := p.store.SetMergeStatuses(ctx, updates); err != nil {
		return err
	}
	p.runnable.NotifyRunnable()
	for _, update := range updates {
		p.changes.byID(ctx, update.JobID)
	}
	return nil
}

// LaterRounds reports the worker's later rounds carrying this job's changes.
func (p *PromotionStore) LaterRounds(ctx context.Context, jobID string) ([]string, bool, error) {
	if p == nil || p.store == nil {
		return nil, false, fmt.Errorf("worker queue store not configured")
	}
	return p.store.LaterRounds(ctx, jobID)
}

// CommitPromotion finalizes one landed promotion under its lease.
func (p *PromotionStore) CommitPromotion(ctx context.Context, jobID, claimToken string, commit PromotionCommit) error {
	if p == nil || p.store == nil {
		return fmt.Errorf("worker queue store not configured")
	}
	if err := p.store.CommitPromotion(ctx, jobID, claimToken, commit); err != nil {
		return err
	}
	p.changes.byID(ctx, jobID)
	p.runnable.NotifyRunnable()
	for _, update := range commit.ChildStatuses {
		p.changes.byID(ctx, update.JobID)
	}
	return nil
}

// ClearWorkerWorkspace clears branch metadata after merge or abort.
func (p *PromotionStore) ClearWorkerWorkspace(ctx context.Context, jobID string) error {
	if p == nil || p.store == nil {
		return nil
	}
	if err := p.store.ClearWorkerWorkspace(ctx, jobID); err != nil {
		return err
	}
	p.changes.byID(ctx, jobID)
	p.runnable.NotifyRunnable()
	return nil
}
