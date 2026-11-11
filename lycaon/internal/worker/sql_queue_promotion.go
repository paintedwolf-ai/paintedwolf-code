package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/worker/jobstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// BeginMergeApply atomically leases a pending branch for apply.
func (q *sqlQueuePromotion) BeginMergeApply(ctx context.Context, jobID string) (string, bool, error) {
	if q == nil || q.sqlQueueCore == nil || q.store == nil {
		return "", false, fmt.Errorf("worker queue store not configured")
	}
	return q.store.BeginMergeApply(ctx, jobID)
}

// RenewMergeApply extends a live merge-apply lease.
func (q *sqlQueuePromotion) RenewMergeApply(ctx context.Context, jobID, claimToken string) (bool, error) {
	if q == nil || q.sqlQueueCore == nil || q.store == nil {
		return false, fmt.Errorf("worker queue store not configured")
	}
	return q.store.RenewMergeApply(ctx, jobID, claimToken)
}

// ReleaseMergeApply returns an applying branch to pending under its claim.
func (q *sqlQueuePromotion) ReleaseMergeApply(ctx context.Context, jobID, claimToken string) error {
	if q == nil || q.sqlQueueCore == nil || q.store == nil {
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
func (q *sqlQueuePromotion) ReclaimExpiredMergeApply(ctx context.Context, jobID string) (string, bool, error) {
	if q == nil || q.sqlQueueCore == nil || q.store == nil {
		return "", false, fmt.Errorf("worker queue store not configured")
	}
	return q.store.ReclaimExpiredMergeApply(ctx, jobID)
}

// ListPendingOverlays returns completed jobs whose overlays await promotion.
func (q *sqlQueuePromotion) ListPendingOverlays(ctx context.Context) ([]PendingOverlay, error) {
	if q == nil || q.sqlQueueCore == nil || q.store == nil {
		return nil, fmt.Errorf("worker queue store not configured")
	}
	return q.store.ListPendingOverlays(ctx)
}

// ListMergeApplying returns job ids whose merge status is applying.
func (q *sqlQueuePromotion) ListMergeApplying(ctx context.Context) ([]string, error) {
	if q == nil || q.sqlQueueCore == nil || q.store == nil {
		return nil, fmt.Errorf("worker queue store not configured")
	}
	return q.store.ListMergeApplying(ctx)
}

// SetMergeStatus records coordinator merge state for a worker branch.
func (q *sqlQueuePromotion) SetMergeStatus(ctx context.Context, jobID string, status api.WorkerMergeStatus) error {
	if q == nil || q.sqlQueueCore == nil || q.store == nil {
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
func (q *sqlQueuePromotion) SetMergeStatuses(ctx context.Context, updates []MergeStatusUpdate) error {
	if q == nil || q.sqlQueueCore == nil || q.store == nil {
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
func (q *sqlQueuePromotion) CommitPromotion(ctx context.Context, jobID, claimToken string, commit PromotionCommit) error {
	if q == nil || q.sqlQueueCore == nil || q.store == nil {
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
func (q *sqlQueuePromotion) ClearWorkerWorkspace(ctx context.Context, jobID string) error {
	if q == nil || q.sqlQueueCore == nil || q.store == nil {
		return nil
	}
	if err := q.store.ClearWorkerWorkspace(ctx, jobID); err != nil {
		return err
	}
	q.refreshBoardByID(ctx, jobID)
	q.NotifyRunnable()
	return nil
}

// GetLatestByChildSessionID returns the newest run for a child session.
func (q *sqlQueuePromotion) GetLatestByChildSessionID(ctx context.Context, childSessionID string) (*api.WorkerTask, bool) {
	if q == nil || q.sqlQueueCore == nil || q.store == nil {
		return nil, false
	}
	return q.store.GetLatestByChildSessionID(ctx, childSessionID)
}

// List returns tasks for a project.
func (q *sqlQueuePromotion) List(ctx context.Context, projectID string, status ...api.WorkerStatus) ([]api.WorkerTask, error) {
	tasks, err := q.store.List(ctx, projectID, status...)
	q.decorateWorkspacePreparations(tasks)
	return tasks, err
}

func (q *sqlQueuePromotion) ListPendingOutcomes(ctx context.Context) ([]api.WorkerTask, error) {
	return q.store.ListPendingOutcomes(ctx)
}

func (q *sqlQueuePromotion) MarkOutcomeDelivered(ctx context.Context, jobID string) error {
	return q.store.MarkOutcomeDelivered(ctx, jobID)
}

// ListBySession returns tasks for a project scoped to a coordinator session.
func (q *sqlQueuePromotion) ListBySession(ctx context.Context, projectID, sessionID string, status ...api.WorkerStatus) ([]api.WorkerTask, error) {
	tasks, err := q.store.ListBySession(ctx, projectID, sessionID, status...)
	q.decorateWorkspacePreparations(tasks)
	return tasks, err
}

// ListByWorkflowRunID returns tasks bound to a workflow run.
func (q *sqlQueuePromotion) ListByWorkflowRunID(ctx context.Context, runID string, status ...api.WorkerStatus) ([]api.WorkerTask, error) {
	return q.store.ListByWorkflowRunID(ctx, runID, status...)
}

func (q *sqlQueuePromotion) ListCancellationRequestsByRunID(ctx context.Context, runID string) ([]api.WorkerTask, error) {
	return q.store.ListCancellationRequestsByRunID(ctx, runID)
}

func (q *sqlQueuePromotion) ListByWorkspacePath(ctx context.Context, workspacePath string, status ...api.WorkerStatus) ([]api.WorkerTask, error) {
	return q.store.ListByWorkspacePath(ctx, workspacePath, status...)
}

// ListPendingOverlayPromote returns completed write workers awaiting overlay promote.
func (q *sqlQueuePromotion) ListPendingOverlayPromote(ctx context.Context, sessionID string) ([]api.WorkerTask, error) {
	tasks, err := q.ListBySession(ctx, "", sessionID, api.WorkerStatusComplete)
	if err != nil {
		return nil, err
	}
	var pending []api.WorkerTask
	for _, task := range tasks {
		if !task.EffectiveScope().IsWrite() || strings.TrimSpace(task.WorkspaceRoot) == "" {
			continue
		}
		if task.MergeStatus != api.WorkerMergeStatusPending {
			continue
		}
		pending = append(pending, task)
	}
	return pending, nil
}

// ListLiveOverlaysForSession returns actionable write overlays.
func (q *sqlQueuePromotion) ListLiveOverlaysForSession(ctx context.Context, sessionID string) ([]api.WorkerTask, error) {
	tasks, err := q.ListBySession(ctx, "", sessionID,
		api.WorkerStatusPending, api.WorkerStatusRunning, api.WorkerStatusWaiting, api.WorkerStatusComplete, api.WorkerStatusHeld)
	if err != nil {
		return nil, err
	}
	return filterLiveOverlays(tasks), nil
}

// Get returns a task within a bounded lookup for callers without a context.
func (q *sqlQueueCore) Get(jobID string) (*api.WorkerTask, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), sqlQueueGetTimeout)
	defer cancel()
	return q.Lookup(ctx, jobID)
}

// Lookup returns a task under the caller's context.
func (q *sqlQueueCore) Lookup(ctx context.Context, jobID string) (*api.WorkerTask, bool) {
	task, ok := q.store.getTask(ctx, jobID)
	if ok && task != nil {
		q.mu.Lock()
		task.WorkspacePreparation = q.preparations[jobID]
		q.mu.Unlock()
	}
	return task, ok
}

// TaskReceipt resolves a durable native-tool receipt before mutable spawn checks.
func (q *sqlQueueCore) TaskReceipt(ctx context.Context, sessionID, callID string) (*api.WorkerTask, error) {
	row, err := q.store.queries.GetWorkerJobBySourceToolCall(ctx, db.GetWorkerJobBySourceToolCallParams{
		ParentSessionID: db.NullString(sessionID), SourceToolCallID: callID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return jobstate.FromRow(ctx, q.store.db, row)
}
