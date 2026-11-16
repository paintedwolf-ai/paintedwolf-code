package worker

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/worker/jobstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// CancelJob finalizes a job as canceled (CAS on non-terminal status). A
// non-empty keptCheckpoint keeps the stopped round's changes pending.
func (s *SQLStore) CancelJob(ctx context.Context, id string, result *api.WorkerResult, keptCheckpoint string) (bool, error) {
	if result == nil {
		result = &api.WorkerResult{Status: "canceled"}
	}
	resultJSON, err := db.MarshalJSON(result)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	job, err := q.GetWorkerJob(ctx, id)
	if db.IsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	completedAt := db.FormatTime(time.Now().UTC())
	n, err := q.CancelWorkerJob(ctx, db.CancelWorkerJobParams{
		ResultJson:     resultJSON,
		CompletedAt:    sql.NullString{String: completedAt, Valid: true},
		KeptCheckpoint: keptCheckpoint,
		ID:             id,
	})
	if err != nil || n != 1 {
		return n == 1, err
	}
	claimToken := db.StringFromNull(job.ClaimToken)
	if claimToken != "" {
		attemptRows, attemptErr := q.CompleteWorkerAttempt(ctx, db.CompleteWorkerAttemptParams{
			Status:      "canceled",
			Error:       "canceled",
			FailureJson: sql.NullString{},
			CompletedAt: sql.NullString{String: completedAt, Valid: true},
			WorkerJobID: id,
			ClaimToken:  claimToken,
		})
		if attemptErr != nil {
			return false, attemptErr
		}
		if attemptRows != 1 {
			return false, fmt.Errorf("running worker %s missing active attempt", id)
		}
	}
	if err := insertCanceledWorkerResult(ctx, tx, id, claimToken, resultJSON, completedAt); err != nil {
		return false, err
	}
	if _, err := q.SettleWorkerRoundByJob(ctx, db.SettleWorkerRoundByJobParams{SettledAt: db.NullString(completedAt), CheckpointID: keptCheckpoint, JobID: id}); err != nil {
		return false, err
	}
	// A stopped worker's unanswered question goes with its round.
	if err := q.DeleteWorkerDecisionByJob(ctx, id); err != nil {
		return false, err
	}
	if _, err := q.CancelWorkerWaitLeases(ctx, db.CancelWorkerWaitLeasesParams{
		UpdatedAt: completedAt, ResolvedAt: sql.NullString{String: completedAt, Valid: true},
		WorkerJobID: sql.NullString{String: id, Valid: true},
	}); err != nil {
		return false, err
	}
	if err := s.jobs.EnqueueJobEventTx(ctx, tx, s.outbox, id); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	s.notify()
	return true, nil
}

func insertCanceledWorkerResult(ctx context.Context, tx *sql.Tx, jobID, claimToken string, resultJSON sql.NullString, createdAt string) error {
	return db.New(tx).InsertCanceledWorkerResult(ctx, db.InsertCanceledWorkerResultParams{
		ID: uuid.NewString(), WorkerJobID: jobID, ClaimToken: claimToken,
		ResultJson: resultJSON, CreatedAt: createdAt,
	})
}

// ReleaseAdmittedJob makes a held job whose round is recorded claimable.
func (s *SQLStore) ReleaseAdmittedJob(ctx context.Context, id string) (bool, error) {
	return s.casInTx(ctx, id, func(q *db.Queries) (int64, error) {
		return q.ReleaseAdmittedWorkerJob(ctx, id)
	})
}

// HoldJob moves a pending job to held (CAS on pending).
func (s *SQLStore) HoldJob(ctx context.Context, id string) (bool, error) {
	return s.casInTx(ctx, id, func(q *db.Queries) (int64, error) {
		return q.HoldWorkerJob(ctx, id)
	})
}
