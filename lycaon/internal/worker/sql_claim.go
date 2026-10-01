package worker

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// CountRunning returns the number of running jobs for an execution target.
func (s *SQLStore) CountRunning(ctx context.Context, target api.ExecutionTarget) (int, error) {
	n, err := s.queries.CountRunningWorkerJobs(ctx, string(target))
	return int(n), err
}

// ClaimNext admits the selected session before atomically taking its job.
func (s *SQLStore) ClaimNext(ctx context.Context, req ClaimRequest, maxRunning int, admission TaskAdmission) (*api.WorkerTask, error) {
	target := req.ExecutionTarget
	if target == "" {
		target = api.ExecutionTargetLocal
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if maxRunning > 0 {
			n, err := s.CountRunning(ctx, target)
			if err != nil {
				return nil, err
			}
			if n >= maxRunning {
				return nil, ErrMaxWorkers
			}
		}
		next, err := s.queries.NextPendingWorkerJob(ctx, db.NextPendingWorkerJobParams{ExecutionTarget: string(target), ProjectID: req.ProjectID})
		if db.IsNoRows(err) {
			return nil, ErrNoPendingJobs
		}
		if err != nil {
			return nil, err
		}
		var task *api.WorkerTask
		claim := func() error {
			var err error
			task, err = s.claimJob(ctx, next.ID, req, maxRunning)
			return err
		}
		if admission == nil {
			err = claim()
		} else {
			err = admission.WithTaskAdmission(ctx, api.WorkerTask{ParentSessionID: db.StringFromNull(next.ParentSessionID)}, claim)
		}
		if errors.Is(err, ErrNoPendingJobs) {
			continue
		}
		return task, err
	}
}

func (s *SQLStore) claimJob(ctx context.Context, id string, req ClaimRequest, maxRunning int) (*api.WorkerTask, error) {
	now := time.Now().UTC()
	claimToken := uuid.NewString()
	var task *api.WorkerTask
	claimErr := s.inTx(ctx, func(q *db.Queries, tx *sql.Tx) error {
		claimed, err := q.ClaimWorkerJob(ctx, db.ClaimWorkerJobParams{
			ID: id, MaxRunning: int64(maxRunning),
			StartedAt:      db.NullString(db.FormatTime(now)),
			ClaimedBy:      db.NullString(req.ClaimedBy),
			ClaimToken:     db.NullString(claimToken),
			HeartbeatAt:    db.NullString(db.FormatTime(now)),
			LeaseExpiresAt: db.NullString(db.FormatTime(now.Add(workerClaimLease))),
		})
		if err != nil {
			return err
		}
		if err := q.InsertWorkerAttempt(ctx, db.InsertWorkerAttemptParams{
			ID:          uuid.NewString(),
			ClaimToken:  claimToken,
			StartedAt:   db.FormatTime(now),
			WorkerJobID: claimed,
		}); err != nil {
			return err
		}
		if err := s.emitJobTx(ctx, tx, claimed); err != nil {
			return err
		}
		row, err := q.GetWorkerJob(ctx, claimed)
		if err != nil {
			return err
		}
		task, err = workerTaskFromRow(ctx, tx, row)
		return err
	})
	if db.IsNoRows(claimErr) {
		return nil, ErrNoPendingJobs
	}
	if claimErr != nil {
		return nil, claimErr
	}
	return task, nil
}
