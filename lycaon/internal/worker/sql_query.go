package worker

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/worker/jobstate"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"time"
)

// List returns jobs for a project with optional status filter.
func (s *SQLStore) List(ctx context.Context, projectID string, statuses ...api.WorkerStatus) ([]api.WorkerTask, error) {
	return s.list(ctx, workerListScope{projectID: projectID}, statuses...)
}

// ListBySession returns jobs for a project scoped to a coordinator session.
func (s *SQLStore) ListBySession(ctx context.Context, projectID, sessionID string, statuses ...api.WorkerStatus) ([]api.WorkerTask, error) {
	return s.list(ctx, workerListScope{projectID: projectID, sessionID: strings.TrimSpace(sessionID)}, statuses...)
}

// ListByWorkflowRunID returns jobs bound to a workflow run.
func (s *SQLStore) ListByWorkflowRunID(ctx context.Context, runID string, statuses ...api.WorkerStatus) ([]api.WorkerTask, error) {
	return s.list(ctx, workerListScope{workflowRunID: strings.TrimSpace(runID)}, statuses...)
}

func (s *SQLStore) ListCancellationRequestsByRunID(ctx context.Context, runID string) ([]api.WorkerTask, error) {
	return s.list(ctx, workerListScope{workflowRunID: strings.TrimSpace(runID), cancellationRequestedOnly: true},
		api.WorkerStatusPending, api.WorkerStatusRunning, api.WorkerStatusWaiting, api.WorkerStatusHeld, api.WorkerStatusCanceled)
}

// ListByWorkspacePath returns jobs sharing one workspace bucket.
func (s *SQLStore) ListByWorkspacePath(ctx context.Context, workspacePath string, statuses ...api.WorkerStatus) ([]api.WorkerTask, error) {
	return s.list(ctx, workerListScope{workspaceKey: enginepaths.ProjectKey(workspacePath)}, statuses...)
}

// ListPendingOutcomes returns terminal or suspended jobs whose side effects are unacknowledged.
func (s *SQLStore) ListPendingOutcomes(ctx context.Context) ([]api.WorkerTask, error) {
	ids, err := s.queries.ListPendingWorkerOutcomeIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]api.WorkerTask, 0, len(ids))
	for _, id := range ids {
		task, ok := s.getTask(ctx, id)
		if !ok {
			return nil, fmt.Errorf("pending worker outcome %s missing", id)
		}
		out = append(out, *task)
	}
	return out, nil
}

// MarkOutcomeDelivered acknowledges all post-terminal side effects for a job.
func (s *SQLStore) MarkOutcomeDelivered(ctx context.Context, jobID string) error {
	return s.queries.MarkWorkerOutcomeDelivered(ctx, db.MarkWorkerOutcomeDeliveredParams{
		WorkerJobID: jobID,
		DeliveredAt: db.FormatTime(time.Now().UTC()),
	})
}

type workerListScope struct {
	projectID, sessionID, workflowRunID, workspaceKey string
	cancellationRequestedOnly                         bool
}

func (s *SQLStore) list(ctx context.Context, scope workerListScope, statuses ...api.WorkerStatus) ([]api.WorkerTask, error) {
	var rows []db.WorkerJobs
	var err error
	filter := make([]string, 0, len(statuses))
	for _, status := range statuses {
		filter = append(filter, string(status))
	}
	raw, err := db.MarshalJSON(filter)
	if err != nil {
		return nil, err
	}
	switch {
	case scope.sessionID != "":
		rows, err = s.queries.ListSessionWorkerJobs(ctx, db.ListSessionWorkerJobsParams{ParentSessionID: db.NullString(scope.sessionID), StatusesJson: raw.String})
	case scope.workflowRunID != "":
		rows, err = s.queries.ListWorkflowWorkerJobs(ctx, db.ListWorkflowWorkerJobsParams{WorkflowRunID: db.NullString(scope.workflowRunID), StatusesJson: raw.String})
	case scope.workspaceKey != "":
		rows, err = s.queries.ListWorkspaceWorkerJobs(ctx, db.ListWorkspaceWorkerJobsParams{WorkspaceKey: scope.workspaceKey, StatusesJson: raw.String})
	case scope.projectID != "":
		rows, err = s.queries.ListProjectWorkerJobs(ctx, db.ListProjectWorkerJobsParams{ProjectID: scope.projectID, StatusesJson: raw.String})
	default:
		rows, err = s.queries.ListWorkerJobs(ctx, raw.String)
	}
	if err != nil {
		return nil, err
	}
	out := make([]api.WorkerTask, 0, len(rows))
	for _, r := range rows {
		if scope.cancellationRequestedOnly && !r.CancelRequestedAt.Valid {
			continue
		}
		if (scope.projectID != "" && r.ProjectID != scope.projectID) || (scope.workflowRunID != "" && r.WorkflowRunID.String != scope.workflowRunID) {
			continue
		}
		task, err := jobstate.FromRow(ctx, s.db, r)
		if err != nil {
			return nil, err
		}
		out = append(out, *task)
	}
	return out, nil
}

func (s *SQLStore) getTask(ctx context.Context, id string) (*api.WorkerTask, bool) {
	row, err := s.queries.GetWorkerJob(ctx, id)
	if err != nil {
		return nil, false
	}
	task, err := jobstate.FromRow(ctx, s.db, row)
	if err != nil {
		return nil, false
	}
	return task, true
}

// GetLatestByChildSessionID returns the newest run for a child session.
func (s *SQLStore) GetLatestByChildSessionID(ctx context.Context, childSessionID string) (*api.WorkerTask, bool) {
	childSessionID = strings.TrimSpace(childSessionID)
	if childSessionID == "" {
		return nil, false
	}
	row, err := s.queries.GetLatestWorkerJobByChildSession(ctx, db.NullString(childSessionID))
	if err != nil {
		return nil, false
	}
	task, err := jobstate.FromRow(ctx, s.db, row)
	if err != nil {
		return nil, false
	}
	return task, true
}
