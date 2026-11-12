package workerrecords

import (
	"context"
	"database/sql"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
)

// Preparation references one workspace preparation for a worker.
type Preparation struct {
	SourceResultID    string `json:"source_result_id,omitempty"`
	PromotionRef      string `json:"promotion_ref,omitempty"`
	PreparationRef    string `json:"preparation_ref,omitempty"`
	BindingGeneration int64  `json:"binding_generation"`
}

// PreparationReceipt names a recorded preparation.
type PreparationReceipt struct {
	WorkerID   string `json:"worker_id"`
	Generation int64  `json:"generation"`
}

// RecordPreparation appends the worker's next workspace preparation. Binding
// generations never go backwards.
func (s *Store) RecordPreparation(ctx context.Context, actor Actor, op Op, workerID string, prep Preparation) (PreparationReceipt, error) {
	prep.SourceResultID = strings.TrimSpace(prep.SourceResultID)
	prep.PromotionRef = strings.TrimSpace(prep.PromotionRef)
	prep.PreparationRef = strings.TrimSpace(prep.PreparationRef)
	if prep.BindingGeneration < 0 {
		return PreparationReceipt{}, ErrInvalidPreparation
	}
	return run(ctx, s, actor, op, "record_preparation", []any{workerID, prep}, func(q *db.Queries, _ *sql.Tx) (PreparationReceipt, error) {
		w, err := coordinatorWorker(ctx, q, actor, workerID)
		if err != nil {
			return PreparationReceipt{}, err
		}
		if prep.SourceResultID != "" {
			owner, err := q.GetWorkerResultOwner(ctx, prep.SourceResultID)
			if db.IsNoRows(err) || (err == nil && owner.WorkerID != w.ID) {
				return PreparationReceipt{}, ErrResultNotFound
			}
			if err != nil {
				return PreparationReceipt{}, err
			}
		}
		generation := int64(1)
		latest, err := q.GetLatestWorkerWorkspacePreparation(ctx, w.ID)
		switch {
		case err == nil:
			if prep.BindingGeneration < latest.BindingGeneration {
				return PreparationReceipt{}, ErrInvalidPreparation
			}
			generation = latest.Generation + 1
		case !db.IsNoRows(err):
			return PreparationReceipt{}, err
		}
		if err := q.InsertWorkerWorkspacePreparation(ctx, db.InsertWorkerWorkspacePreparationParams{
			WorkerID: w.ID, Generation: generation, SourceResultID: nullString(prep.SourceResultID),
			PromotionRef: prep.PromotionRef, PreparationRef: prep.PreparationRef,
			BindingGeneration: prep.BindingGeneration, OperationID: op.ID, CreatedAt: s.stamp(),
		}); err != nil {
			return PreparationReceipt{}, err
		}
		return PreparationReceipt{WorkerID: w.ID, Generation: generation}, nil
	})
}
