package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/worker/jobstate"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

// Complete commits the result only while the claim remains current.
func (q *SQLQueue) Complete(ctx context.Context, claimed *api.WorkerTask, result api.WorkerResult) (bool, error) {
	return admitWorkerTransition(ctx, q, claimed, func() (bool, error) {
		return q.completeAdmitted(ctx, claimed, result)
	})
}

func (q *SQLQueue) completeAdmitted(ctx context.Context, claimed *api.WorkerTask, result api.WorkerResult) (bool, error) {
	if claimed == nil || strings.TrimSpace(claimed.ID) == "" || strings.TrimSpace(claimed.ClaimToken) == "" {
		return false, fmt.Errorf("claimed worker task required")
	}
	task := claimed
	jobID := claimed.ID
	claimToken := claimed.ClaimToken
	// Overlay status and the terminal result share one transaction.
	mergeStatus := api.WorkerMergeStatus("")
	teardown := false
	if task.EffectiveScope().IsWrite() {
		if strings.TrimSpace(task.WorkspaceRoot) == "" {
			if loaded, ok := q.store.getTask(ctx, jobID); ok && loaded != nil {
				loaded.ClaimToken = claimToken
				task = loaded
			}
		}
		switch {
		case strings.TrimSpace(result.Status) == "needs_decision":
			// Decision branches stay attached and non-promotable.
		case session.OverlayAwaitingPromote(ctx, task):
			mergeStatus = api.WorkerMergeStatusPending
		case q.overlayDivergesFromProject(ctx, task):
			// Preserve untracked overlay divergence for review.
			workerWorkspaceLog.Warn("write overlay retained despite empty baseline diff",
				"job_id", jobID, "branch_root", strings.TrimSpace(task.WorkspaceRoot))
			mergeStatus = api.WorkerMergeStatusPending
		default:
			mergeStatus = api.WorkerMergeStatusAborted
			teardown = true
		}
	}
	overlayPath := ""
	// A missing baseline or budget-exceeded overlay leaves the overlay unsealed for review.
	if mergeStatus == api.WorkerMergeStatusPending && strings.TrimSpace(task.WorkspaceOverlayPath) == "" && strings.TrimSpace(task.WorkspaceBaselinePath) != "" {
		captured, err := q.captureOverlay(ctx, task)
		if err != nil {
			if errors.Is(err, workspacebaseline.ErrOverlayBudgetExceeded) {
				workerWorkspaceLog.Warn("write overlay exceeds budget; leaving unsealed for review",
					"job_id", task.ID, "error", err)
			} else {
				return false, err
			}
		} else {
			overlayPath = captured
		}
	}
	completion, err := q.store.CompleteJob(ctx, jobID, result, claimToken, mergeStatus, teardown, overlayPath)
	if err != nil || !completion.Committed || completion.Suspended {
		q.discardOverlay(ctx, overlayPath)
	}
	if err != nil {
		return false, err
	}
	if !completion.Committed {
		return false, nil
	}
	if teardown && !completion.Suspended {
		q.destroyWorkerWorkspace(ctx, task)
	}
	q.refreshBoardByID(ctx, jobID)
	return true, nil
}

// Fail commits a failure only while the claim remains current.
func (q *SQLQueue) Fail(ctx context.Context, claimed *api.WorkerTask, runErr error) (bool, error) {
	return admitWorkerTransition(ctx, q, claimed, func() (bool, error) {
		return q.failAdmitted(ctx, claimed, runErr)
	})
}

func (q *SQLQueue) failAdmitted(ctx context.Context, claimed *api.WorkerTask, runErr error) (bool, error) {
	if claimed == nil || strings.TrimSpace(claimed.ID) == "" || strings.TrimSpace(claimed.ClaimToken) == "" {
		return false, fmt.Errorf("claimed worker task required")
	}
	msg := ""
	var failure *api.WorkerFailure
	if runErr != nil {
		msg = runErr.Error()
		if q.failureCatalog != nil {
			rendered := q.failureCatalog.RenderExecuteFailure(runErr)
			failure = &rendered
		}
	}
	task := claimed
	if strings.TrimSpace(task.WorkspaceRoot) == "" {
		if loaded, ok := q.store.getTask(ctx, claimed.ID); ok && loaded != nil {
			loaded.ClaimToken = claimed.ClaimToken
			task = loaded
		}
	}
	won, err := q.store.FailJob(ctx, claimed, msg, failure)
	if err != nil {
		return false, err
	}
	if !won {
		return false, nil
	}
	if task != nil {
		q.destroyWorkerWorkspace(ctx, task)
	}
	q.refreshBoardByID(ctx, claimed.ID)
	return true, nil
}

// Retry records the failed attempt and keeps the child/workspace for resume.
func (q *SQLQueue) Retry(ctx context.Context, claimed *api.WorkerTask, runErr error) (bool, error) {
	return admitWorkerTransition(ctx, q, claimed, func() (bool, error) {
		return q.retryAdmitted(ctx, claimed, runErr)
	})
}

func (q *SQLQueue) retryAdmitted(ctx context.Context, claimed *api.WorkerTask, runErr error) (bool, error) {
	if claimed == nil || strings.TrimSpace(claimed.ID) == "" || strings.TrimSpace(claimed.ClaimToken) == "" {
		return false, fmt.Errorf("claimed worker task required")
	}
	msg := ""
	var failure *api.WorkerFailure
	if runErr != nil {
		msg = runErr.Error()
		if q.failureCatalog != nil {
			rendered := q.failureCatalog.RenderExecuteFailure(runErr)
			failure = &rendered
		}
	}
	won, err := q.store.RetryJob(ctx, claimed, msg, failure)
	if err != nil || !won {
		return won, err
	}
	q.refreshBoardByID(ctx, claimed.ID)
	q.NotifyRunnable()
	return true, nil
}

func (q *SQLQueue) Park(ctx context.Context, claimed *api.WorkerTask) (bool, error) {
	return admitWorkerTransition(ctx, q, claimed, func() (bool, error) {
		return q.parkAdmitted(ctx, claimed)
	})
}

func (q *SQLQueue) parkAdmitted(ctx context.Context, claimed *api.WorkerTask) (bool, error) {
	won, err := q.store.ParkJob(ctx, claimed)
	if err == nil && won {
		q.refreshBoardByID(ctx, claimed.ID)
	}
	return won, err
}

// ResumeReadyWaits atomically expires deadlines and makes every resolved worker runnable.
func (q *SQLQueue) ResumeReadyWaits(ctx context.Context) (int, error) {
	now := db.FormatTime(time.Now().UTC())
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := q.store.queries.WithTx(tx)
	if _, err := queries.ExpireWorkerWaitLeases(ctx, db.ExpireWorkerWaitLeasesParams{
		UpdatedAt: now, ResolvedAt: sql.NullString{String: now, Valid: true}, ExpiredBefore: sql.NullString{String: now, Valid: true},
	}); err != nil {
		return 0, err
	}
	ready, err := queries.ListReadyWaitingWorkerJobIDs(ctx)
	if err != nil {
		return 0, err
	}
	jobIDs := make([]string, 0, len(ready))
	for _, value := range ready {
		if jobID := db.StringFromNull(value); jobID != "" {
			jobIDs = append(jobIDs, jobID)
		}
	}
	for _, jobID := range jobIDs {
		changed, err := queries.ResumeWaitingWorkerJob(ctx, jobID)
		if err != nil {
			return 0, err
		}
		if changed == 1 {
			if err := jobstate.EnqueueJobEventTx(ctx, tx, q.store.outbox, jobID); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if len(jobIDs) > 0 {
		q.NotifyRunnable()
	}
	return len(jobIDs), nil
}
