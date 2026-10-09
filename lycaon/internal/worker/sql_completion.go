package worker

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/lycaon/lycaon/internal/worker/jobstate"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

// JobCompletion records the durable outcome of settling one worker claim.
type JobCompletion struct {
	Committed bool
	Suspended bool
}

// CompleteJob commits the result, overlay manifest, and merge status together.
func (s *SQLStore) CompleteJob(ctx context.Context, id string, result api.WorkerResult, claimToken string, mergeStatus api.WorkerMergeStatus, clearWorkspace bool, overlayPath string) (JobCompletion, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return JobCompletion{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	completedAt := db.FormatTime(time.Now().UTC())
	_, decisionErr := q.GetWorkerDecisionByJob(ctx, id)
	if decisionErr != nil && !db.IsNoRows(decisionErr) {
		return JobCompletion{}, decisionErr
	}
	suspended := decisionErr == nil
	if suspended {
		result.Status = string(api.WorkerSummaryStatusNeedsDecision)
		mergeStatus = ""
		clearWorkspace = false
		overlayPath = ""
	} else if result.Status == string(api.WorkerSummaryStatusNeedsDecision) {
		return JobCompletion{}, fmt.Errorf("worker %s requested suspension without a pending decision", id)
	}
	resultJSON, err := db.MarshalJSON(result)
	if err != nil {
		return JobCompletion{}, err
	}
	// Attach the overlay before completion retires the claim.
	if overlayPath != "" {
		overlayID, err := workspacebaseline.ID(overlayPath)
		if err != nil {
			return JobCompletion{}, err
		}
		n, err := q.SetWorkerJobOverlay(ctx, db.SetWorkerJobOverlayParams{
			WorkspaceOverlayID: db.NullString(overlayID), ID: id, ClaimToken: db.NullString(claimToken),
		})
		if err != nil {
			return JobCompletion{}, err
		}
		if n != 1 {
			return JobCompletion{}, nil
		}
	}
	var n int64
	if suspended {
		n, err = q.SuspendWorkerJobForDecision(ctx, db.SuspendWorkerJobForDecisionParams{
			ResultJson: resultJSON, CompletedAt: db.NullString(completedAt),
			ID: id, ClaimToken: db.NullString(claimToken),
		})
	} else {
		n, err = q.CompleteWorkerJob(ctx, db.CompleteWorkerJobParams{
			ResultJson: resultJSON, CompletedAt: db.NullString(completedAt),
			ID: id, ClaimToken: db.NullString(claimToken),
		})
	}
	if err != nil {
		return JobCompletion{}, err
	}
	if n == 0 {
		return JobCompletion{}, nil
	}
	attemptStatus := "succeeded"
	if suspended {
		attemptStatus = "suspended"
	}
	attemptRows, err := q.CompleteWorkerAttempt(ctx, db.CompleteWorkerAttemptParams{
		Status: attemptStatus, Error: "", FailureJson: sql.NullString{},
		CompletedAt: db.NullString(completedAt), WorkerJobID: id, ClaimToken: claimToken,
	})
	if err != nil {
		return JobCompletion{}, err
	}
	if attemptRows != 1 {
		return JobCompletion{}, fmt.Errorf("running worker %s missing active attempt", id)
	}
	if !suspended {
		if err := q.InsertWorkerResult(ctx, db.InsertWorkerResultParams{
			ID: uuid.NewString(), WorkerJobID: id, Status: "complete", ResultJson: resultJSON,
			Error: "", FailureJson: sql.NullString{}, CreatedAt: completedAt, ClaimToken: claimToken,
		}); err != nil {
			return JobCompletion{}, err
		}
	}
	if mergeStatus != "" {
		n, err := q.SetWorkerJobMergeStatus(ctx, db.SetWorkerJobMergeStatusParams{
			MergeStatus: db.NullString(string(mergeStatus)),
			ID:          id,
		})
		if err != nil {
			return JobCompletion{}, err
		}
		if n != 1 {
			return JobCompletion{}, fmt.Errorf("worker %s missing after completion", id)
		}
	}
	if clearWorkspace {
		if err := q.ClearWorkerJobWorkspaceRoot(ctx, id); err != nil {
			return JobCompletion{}, err
		}
	}
	if err := jobstate.EnqueueJobEventTx(ctx, tx, s.outbox, id); err != nil {
		return JobCompletion{}, err
	}
	if err := tx.Commit(); err != nil {
		return JobCompletion{}, err
	}
	s.notify()
	return JobCompletion{Committed: true, Suspended: suspended}, nil
}

// FailJob atomically fails the active claim.
func (s *SQLStore) FailJob(ctx context.Context, claimed *api.WorkerTask, errMsg string, failure *api.WorkerFailure) (bool, error) {
	if claimed == nil || strings.TrimSpace(claimed.ID) == "" || strings.TrimSpace(claimed.ClaimToken) == "" {
		return false, fmt.Errorf("claimed worker task required")
	}
	failureJSON, err := db.MarshalJSON(failure)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	completedAt := db.FormatTime(time.Now().UTC())
	n, err := q.FailWorkerJob(ctx, db.FailWorkerJobParams{
		Error:       db.NullString(errMsg),
		FailureJson: failureJSON,
		CompletedAt: sql.NullString{String: completedAt, Valid: true},
		ID:          claimed.ID,
		ClaimToken:  db.NullString(claimed.ClaimToken),
	})
	if err != nil || n != 1 {
		return n == 1, err
	}
	attemptRows, err := q.CompleteWorkerAttempt(ctx, db.CompleteWorkerAttemptParams{
		Status: "terminal_failed", Error: errMsg, FailureJson: failureJSON,
		CompletedAt: sql.NullString{String: completedAt, Valid: true},
		WorkerJobID: claimed.ID, ClaimToken: claimed.ClaimToken,
	})
	if err != nil {
		return false, err
	}
	if attemptRows != 1 {
		return false, fmt.Errorf("running worker %s missing active attempt", claimed.ID)
	}
	if err := q.InsertWorkerResult(ctx, db.InsertWorkerResultParams{
		ID: uuid.NewString(), WorkerJobID: claimed.ID, Status: "failed",
		ResultJson: sql.NullString{}, Error: errMsg, FailureJson: failureJSON,
		CreatedAt: completedAt, ClaimToken: claimed.ClaimToken,
	}); err != nil {
		return false, err
	}
	if err := jobstate.EnqueueJobEventTx(ctx, tx, s.outbox, claimed.ID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	s.notify()
	return true, nil
}

// RetryJob closes one attempt and returns its semantic job to pending.
func (s *SQLStore) RetryJob(ctx context.Context, claimed *api.WorkerTask, errMsg string, failure *api.WorkerFailure) (bool, error) {
	if claimed == nil || strings.TrimSpace(claimed.ID) == "" || strings.TrimSpace(claimed.ClaimToken) == "" {
		return false, fmt.Errorf("claimed worker task required")
	}
	failureJSON, err := db.MarshalJSON(failure)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	rows, err := q.RetryWorkerJob(ctx, db.RetryWorkerJobParams{
		Error: db.NullString(errMsg), FailureJson: failureJSON,
		ID: claimed.ID, ClaimToken: db.NullString(claimed.ClaimToken),
	})
	if err != nil || rows != 1 {
		return rows == 1, err
	}
	completedAt := db.FormatTime(time.Now().UTC())
	attemptRows, err := q.CompleteWorkerAttempt(ctx, db.CompleteWorkerAttemptParams{
		Status: "retryable_failed", Error: errMsg, FailureJson: failureJSON,
		CompletedAt: sql.NullString{String: completedAt, Valid: true},
		WorkerJobID: claimed.ID, ClaimToken: claimed.ClaimToken,
	})
	if err != nil {
		return false, err
	}
	if attemptRows != 1 {
		return false, fmt.Errorf("running worker %s missing active attempt", claimed.ID)
	}
	if err := jobstate.EnqueueJobEventTx(ctx, tx, s.outbox, claimed.ID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	s.notify()
	return true, nil
}

// ParkJob closes the active attempt without completing the semantic job.
func (s *SQLStore) ParkJob(ctx context.Context, claimed *api.WorkerTask) (bool, error) {
	if claimed == nil || strings.TrimSpace(claimed.ID) == "" || strings.TrimSpace(claimed.ClaimToken) == "" {
		return false, fmt.Errorf("claimed worker task required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	now := db.FormatTime(time.Now().UTC())
	q := s.queries.WithTx(tx)
	rows, err := q.ParkWorkerJob(ctx, db.ParkWorkerJobParams{
		ID: claimed.ID, ClaimToken: sql.NullString{String: claimed.ClaimToken, Valid: true},
	})
	if err != nil {
		return false, err
	}
	if rows != 1 {
		return false, nil
	}
	attemptRows, err := q.SuspendWorkerAttempt(ctx, db.SuspendWorkerAttemptParams{
		CompletedAt: sql.NullString{String: now, Valid: true},
		WorkerJobID: claimed.ID, ClaimToken: claimed.ClaimToken,
	})
	if err != nil {
		return false, err
	}
	if attemptRows != 1 {
		return false, fmt.Errorf("running worker %s missing active attempt", claimed.ID)
	}
	if err := jobstate.EnqueueJobEventTx(ctx, tx, s.outbox, claimed.ID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	s.notify()
	return true, nil
}
