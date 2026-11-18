package worker

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/lycaon/lycaon/internal/timelayout"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/workspace"
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
	prepared, err := q.Branches.prepareCompletion(ctx, claimed, result)
	if err != nil {
		return false, err
	}
	task, mergeStatus, teardown := prepared.task, prepared.mergeStatus, prepared.teardown
	overlayPath, checkpointID := prepared.overlayPath, prepared.checkpointID
	jobID := claimed.ID
	criteriaJSON := q.evaluateRoundCriteria(ctx, task, strings.TrimSpace(result.Status) != "needs_decision", result.Summary, result.ChangeReport)
	completion, err := q.store.CompleteJob(ctx, jobID, result, claimed.ClaimToken, mergeStatus, teardown, overlayPath, checkpointID, criteriaJSON)
	if err != nil || !completion.Committed || completion.Suspended {
		q.Branches.discardOverlay(ctx, overlayPath)
	}
	if err != nil || !completion.Committed {
		return false, err
	}
	if teardown && !completion.Suspended {
		q.Branches.destroyWorkerWorkspace(ctx, task)
	}
	q.changes.byID(ctx, jobID)
	if !completion.Suspended {
		q.notifyRoundSettled(ctx, task)
	}
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
	kept := q.Branches.keptCheckpoint(ctx, task)
	criteriaJSON := q.evaluateRoundCriteria(ctx, task, false, "", nil)
	won, err := q.store.FailJob(ctx, claimed, msg, failure, kept, criteriaJSON)
	if err != nil {
		return false, err
	}
	if !won {
		return false, nil
	}
	if task != nil && kept == "" {
		q.Branches.destroyWorkerWorkspace(ctx, task)
	}
	q.changes.byID(ctx, claimed.ID)
	q.notifyRoundSettled(ctx, task)
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
	q.changes.byID(ctx, claimed.ID)
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
		q.changes.byID(ctx, claimed.ID)
	}
	return won, err
}

// ResumeReadyWaits atomically expires deadlines and makes every resolved worker runnable.
func (q *SQLQueue) ResumeReadyWaits(ctx context.Context) (int, error) {
	now := timelayout.Format(time.Now().UTC())
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
			if err := q.store.jobs.EnqueueJobEventTx(ctx, tx, q.store.outbox, jobID); err != nil {
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

// hasLayeredOverlayDivergence answers from a branch's view when it has one: whether the worker changed
// anything, and whether the view could answer.
func hasLayeredOverlayDivergence(ctx context.Context, task *api.WorkerTask) (bool, bool) {
	if task == nil || strings.TrimSpace(task.WorkspaceRoot) == "" {
		return false, false
	}
	paths, handled, err := workspace.OverlayWorkspaceDivergencePaths(ctx, task.WorkspaceRoot)
	if !handled {
		return false, false
	}
	return err != nil || len(paths) > 0, true
}

func (q *SQLQueue) evaluateRoundCriteria(
	ctx context.Context,
	task *api.WorkerTask,
	jobComplete bool,
	summary string,
	report *api.WorkerChangeReport,
) string {
	if task == nil {
		return ""
	}
	q.mu.Lock()
	eval := q.criteriaEvaluator
	q.mu.Unlock()
	if eval == nil {
		return ""
	}
	roots := q.Branches.rootRefs(ctx, task)
	var diverged []string
	if strings.TrimSpace(task.WorkspaceRoot) != "" {
		if paths, handled, err := workspace.OverlayWorkspaceDivergencePaths(ctx, task.WorkspaceRoot); handled && err == nil {
			diverged = paths
		} else {
			diverged = session.OverlayWorkspaceDivergencePaths(task, roots) //nolint:contextcheck // Divergence summary inspects baseline snapshots synchronously.
		}
	}
	projectDir := strings.TrimSpace(task.WorkspacePath)
	if projectDir == "" {
		if refs := roots; len(refs) > 0 {
			projectDir = strings.TrimSpace(refs[0].Path)
		}
	}
	return eval(ctx, task, projectDir, jobComplete, summary, diverged, report)
}

func (q *SQLQueue) notifyRoundSettled(ctx context.Context, task *api.WorkerTask) {
	if task == nil || strings.TrimSpace(task.ParentSessionID) == "" {
		return
	}
	q.mu.Lock()
	onSettled := q.onRoundSettled
	q.mu.Unlock()
	if onSettled != nil {
		onSettled(ctx, task.ParentSessionID)
	}
}
