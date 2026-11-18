package worker

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

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

// ListPendingOverlayPromote returns completed write workers awaiting overlay
// promote. Shelved changes stay unlanded on purpose and are not listed.
func (q *sqlQueuePromotion) ListPendingOverlayPromote(ctx context.Context, sessionID string) ([]api.WorkerTask, error) {
	tasks, err := q.ListBySession(ctx, "", sessionID, api.WorkerStatusComplete)
	if err != nil {
		return nil, err
	}
	shelvedIDs, err := q.store.queries.ListShelvedWorkerJobs(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	shelved := make(map[string]struct{}, len(shelvedIDs))
	for _, id := range shelvedIDs {
		shelved[id] = struct{}{}
	}
	var pending []api.WorkerTask
	for _, task := range tasks {
		if !task.EffectiveScope().IsWrite() || strings.TrimSpace(task.WorkspaceRoot) == "" {
			continue
		}
		if task.MergeStatus != api.WorkerMergeStatusPending {
			continue
		}
		if _, ok := shelved[task.ID]; ok {
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

// LookupByResult resolves a settled round's result handle to the job that produced it.
func (q *sqlQueueCore) LookupByResult(ctx context.Context, resultHandle string) (*api.WorkerTask, bool) {
	if q == nil || q.store == nil {
		return nil, false
	}
	owner, err := q.store.queries.GetWorkerResultOwner(ctx, strings.TrimSpace(resultHandle))
	if err != nil {
		return nil, false
	}
	return q.Lookup(ctx, owner.WorkerJobID)
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
	return q.store.jobs.FromRow(ctx, q.store.db, row)
}
