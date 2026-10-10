package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/pkg/api"
)

type ReviewAssignmentActiveError struct{ JobID string }

func (e *ReviewAssignmentActiveError) Error() string {
	return "review assignment already has active job " + e.JobID
}

// InsertTask inserts a worker job row.
func (s *SQLStore) InsertTask(ctx context.Context, task api.WorkerTask) error {
	if task.ID == "" {
		task.ID = uuid.NewString()
	}
	return s.mutateInTx(ctx, task.ID, func(q *db.Queries) error {
		return insertTask(ctx, q, task)
	})
}

// InsertTaskTx inserts a job in the caller's transaction.
func InsertTaskTx(ctx context.Context, tx *sql.Tx, task api.WorkerTask) error {
	if tx == nil {
		return fmt.Errorf("worker insert transaction required")
	}
	return insertTask(ctx, db.New(tx), task)
}

func insertTask(ctx context.Context, queries *db.Queries, task api.WorkerTask) error {
	if task.ID == "" {
		task.ID = uuid.NewString()
	}
	if task.WorkflowRunID != "" {
		active, err := queries.ActiveWorkflowReviewAssignmentJob(ctx, task.ID)
		if err == nil {
			return &ReviewAssignmentActiveError{JobID: active}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if err := normalizeWorkerInstructions(&task); err != nil {
		return err
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now().UTC()
	}
	if task.Status == "" {
		task.Status = api.WorkerStatusPending
	}
	filesJSON, err := db.MarshalJSON(task.Files)
	if err != nil {
		return err
	}
	resultJSON, err := db.MarshalJSON(task.Result)
	if err != nil {
		return err
	}
	failureJSON, err := db.MarshalJSON(task.Failure)
	if err != nil {
		return err
	}
	scopeJSON, err := db.MarshalJSON(task.Scope)
	if err != nil {
		return err
	}
	err = queries.InsertWorkerJob(ctx, db.InsertWorkerJobParams{
		ID:               task.ID,
		ProjectID:        task.ProjectID,
		WorkspaceRootID:  db.NullString(task.WorkspaceRootID),
		WorkspacePath:    task.WorkspacePath,
		WorkspaceKey:     enginepaths.ProjectKey(task.WorkspacePath),
		DelegationID:     db.NullString(task.DelegationID),
		LegID:            db.NullString(task.LegID),
		ParentSessionID:  db.NullString(task.ParentSessionID),
		SourceToolCallID: task.SourceToolCallID,
		SourceArgsDigest: task.SourceArgsDigest,
		ChildSessionID:   db.NullString(task.ChildSessionID),
		WorkflowRunID:    db.NullString(task.WorkflowRunID),
		WorkflowPhase:    task.WorkflowPhase,
		WorkflowWorkID:   task.WorkflowWorkID,
		ExecutionTarget:  string(task.ExecutionTarget),
		RunnerID:         db.NullString(task.RunnerID),
		ClaimedBy:        db.NullString(task.ClaimedBy),
		ClaimToken:       db.NullString(task.ClaimToken),
		Attempt:          int64(task.Attempt),
		HeartbeatAt:      db.NullTimePtr(task.HeartbeatAt),
		LeaseExpiresAt:   db.NullTimePtr(task.LeaseExpiresAt),
		AgentType:        task.AgentType,
		Status:           string(task.Status),
		SpawnReason:      db.NullString(string(task.SpawnReason)),
		Prompt:           task.Prompt,
		Brief:            task.Brief,
		FilesJson:        filesJSON,
		ScopeJson:        scopeJSON,
		ResultJson:       resultJSON,
		Error:            db.NullString(task.Error),
		FailureJson:      failureJSON,
		CreatedAt:        db.FormatTime(task.CreatedAt),
		StartedAt:        db.NullTimePtr(task.StartedAt),
		CompletedAt:      db.NullTimePtr(task.CompletedAt),
		OverlayID:        db.NullString(task.OverlayID),
		MaxToolLoops:     workerJobMaxToolLoops(task.MaxToolLoops),
		ToolLoopsUsed:    workerJobCount(task.ToolLoopsUsed),
		ToolCallsUsed:    workerJobCount(task.ToolCallsUsed),
	})
	if err != nil {
		return err
	}
	for _, id := range task.AfterWorkers {
		if err := queries.InsertWorkerPrerequisite(ctx, db.InsertWorkerPrerequisiteParams{WorkerJobID: task.ID, PrerequisiteID: id}); err != nil {
			return err
		}
	}
	return nil
}

func workerJobMaxToolLoops(n int) sql.NullInt64 {
	if n <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(n), Valid: true}
}

func workerJobCount(n int) sql.NullInt64 {
	if n <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(n), Valid: true}
}
