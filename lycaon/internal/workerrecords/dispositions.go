package workerrecords

import (
	"context"
	"database/sql"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
)

// Change states a result's changes can reach.
const (
	ChangeLanded   = "landed"
	ChangeRejected = "rejected"
	ChangeShelved  = "shelved"
)

// Disposition is how a result was handled.
type Disposition struct {
	ResultID             string `json:"result_id"`
	WorkerID             string `json:"worker_id"`
	AccountedOperationID string `json:"accounted_operation_id,omitempty"`
	AccountedAt          string `json:"accounted_at,omitempty"`
	ChangeState          string `json:"change_state,omitempty"`
	ChangeOperationID    string `json:"change_operation_id,omitempty"`
	LandedChangeID       string `json:"landed_change_id,omitempty"`
	ShelveReason         string `json:"shelve_reason,omitempty"`
	ChangedAt            string `json:"changed_at,omitempty"`
}

func dispositionFromRow(d db.WorkerResultDispositions) Disposition {
	return Disposition{
		ResultID: d.ResultID, WorkerID: d.WorkerID, AccountedOperationID: d.AccountedOperationID,
		AccountedAt: d.AccountedAt.String, ChangeState: d.ChangeState, ChangeOperationID: d.ChangeOperationID,
		LandedChangeID: d.LandedChangeID.String, ShelveReason: d.ShelveReason, ChangedAt: d.ChangedAt.String,
	}
}

// resultWorker returns the open worker a result belongs to, for its coordinator.
func resultWorker(ctx context.Context, q *db.Queries, actor Actor, resultID string) (db.Workers, db.GetWorkerResultOwnerRow, error) {
	owner, err := q.GetWorkerResultOwner(ctx, strings.TrimSpace(resultID))
	if db.IsNoRows(err) {
		return db.Workers{}, owner, ErrResultNotFound
	}
	if err != nil {
		return db.Workers{}, owner, err
	}
	w, err := coordinatorWorker(ctx, q, actor, owner.WorkerID)
	if err == ErrWorkerNotFound {
		return db.Workers{}, owner, ErrResultNotFound
	}
	return w, owner, err
}

// AccountResult records that the coordinator accounted for a result. It lands
// nothing.
func (s *Store) AccountResult(ctx context.Context, actor Actor, op Op, resultID string) (Disposition, error) {
	return run(ctx, s, actor, op, "account_result", resultID, func(q *db.Queries, _ *sql.Tx) (Disposition, error) {
		w, _, err := resultWorker(ctx, q, actor, resultID)
		if err != nil {
			return Disposition{}, err
		}
		n, err := q.AccountWorkerResult(ctx, db.AccountWorkerResultParams{
			ResultID: resultID, WorkerID: w.ID, OperationID: op.ID, AccountedAt: nullString(s.stamp()),
		})
		if err != nil {
			return Disposition{}, err
		}
		if n == 0 {
			return Disposition{}, ErrAlreadyAccounted
		}
		row, err := q.GetWorkerResultDisposition(ctx, resultID)
		return dispositionFromRow(row), err
	})
}

// Change records what happened to a result's changes.
type Change struct {
	State          string `json:"state"`
	LandedChangeID string `json:"landed_change_id,omitempty"`
	ShelveReason   string `json:"shelve_reason,omitempty"`
}

// SetChange records a result's changes as landed, rejected or shelved.
// Shelved changes may later land or be rejected; landed and rejected are final.
// A landing names the landed-change receipt of the result's own job.
func (s *Store) SetChange(ctx context.Context, actor Actor, op Op, resultID string, change Change) (Disposition, error) {
	change.LandedChangeID = strings.TrimSpace(change.LandedChangeID)
	change.ShelveReason = strings.TrimSpace(change.ShelveReason)
	switch change.State {
	case ChangeLanded:
		if change.LandedChangeID == "" || change.ShelveReason != "" {
			return Disposition{}, ErrInvalidDisposition
		}
	case ChangeRejected:
		if change.LandedChangeID != "" || change.ShelveReason != "" {
			return Disposition{}, ErrInvalidDisposition
		}
	case ChangeShelved:
		if change.LandedChangeID != "" || change.ShelveReason == "" {
			return Disposition{}, ErrInvalidDisposition
		}
	default:
		return Disposition{}, ErrInvalidDisposition
	}
	return run(ctx, s, actor, op, "set_change", []any{resultID, change}, func(q *db.Queries, _ *sql.Tx) (Disposition, error) {
		w, owner, err := resultWorker(ctx, q, actor, resultID)
		if err != nil {
			return Disposition{}, err
		}
		if change.State == ChangeLanded {
			jobID, err := q.GetLandedChangeJobID(ctx, change.LandedChangeID)
			if db.IsNoRows(err) || (err == nil && jobID != owner.WorkerJobID) {
				return Disposition{}, ErrInvalidDisposition
			}
			if err != nil {
				return Disposition{}, err
			}
		}
		n, err := q.SetWorkerResultChangeState(ctx, db.SetWorkerResultChangeStateParams{
			ResultID: resultID, WorkerID: w.ID, ChangeState: change.State, OperationID: op.ID,
			LandedChangeID: nullString(change.LandedChangeID), ShelveReason: change.ShelveReason,
			ChangedAt: nullString(s.stamp()),
		})
		if err != nil {
			return Disposition{}, err
		}
		if n == 0 {
			return Disposition{}, ErrDispositionFinal
		}
		row, err := q.GetWorkerResultDisposition(ctx, resultID)
		return dispositionFromRow(row), err
	})
}

// Dispositions lists the recorded dispositions of a worker's results.
func (s *Store) Dispositions(ctx context.Context, actor Actor, workerID string) ([]Disposition, error) {
	if err := actor.check(); err != nil {
		return nil, err
	}
	w, _, err := loadWorker(ctx, s.q, actor, workerID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListWorkerResultDispositions(ctx, w.ID)
	if err != nil {
		return nil, err
	}
	out := make([]Disposition, 0, len(rows))
	for _, d := range rows {
		out = append(out, dispositionFromRow(d))
	}
	return out, nil
}
