package workflow

import (
	"context"
	"database/sql"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func insertTeardownTx(ctx context.Context, tx *sql.Tx, op *workflowTeardownIntent) error {
	if op == nil {
		return nil
	}
	now := db.FormatTime(time.Now().UTC())
	return db.New(tx).InsertWorkflowTeardownOperation(ctx, db.InsertWorkflowTeardownOperationParams{
		ID: op.ID, RunID: op.RunID, SourceRevision: op.SourceRevision,
		CancelScope: string(op.CancelScope), AbortDelegation: boolInt64(op.AbortDelegation),
		Reason: op.Reason, CreatedAt: now, UpdatedAt: now,
	})
}

func (s *SQLStore) insertTreeTeardownTx(ctx context.Context, tx *sql.Tx, run *api.WorkflowRun, op *workflowTeardownIntent, planned bool, reason string) error {
	if !planned && run != nil {
		op = newWorkflowTeardownIntent(run.ID, run.Revision, workerCancelAll, true, reason)
	}
	if op == nil {
		return nil
	}
	if op.CancelScope == workerCancelAll {
		if err := db.New(tx).RequestWorkflowWorkerCancellation(ctx, db.RequestWorkflowWorkerCancellationParams{
			RequestedAt: db.NullString(db.FormatTime(time.Now().UTC())), WorkflowRunID: db.NullString(run.ID),
		}); err != nil {
			return err
		}
		if err := worker.EnqueueRunJobEventsTx(ctx, tx, s.outbox, run.ID); err != nil {
			return err
		}
	}
	return insertTeardownTx(ctx, tx, op)
}

func boolInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func (s *SQLStore) PendingTeardowns(ctx context.Context) ([]workflowTeardownIntent, error) {
	rows, err := s.queries.ListPendingWorkflowTeardownOperations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]workflowTeardownIntent, 0, len(rows))
	for _, row := range rows {
		out = append(out, workflowTeardownIntent{
			ID: row.ID, RunID: row.RunID, SourceRevision: row.SourceRevision,
			CancelScope: workerCancelScope(row.CancelScope), AbortDelegation: row.AbortDelegation == 1,
			Reason: row.Reason,
		})
	}
	return out, nil
}

func (s *SQLStore) CompleteTeardown(ctx context.Context, operationID string) error {
	return s.queries.CompleteWorkflowTeardownOperation(ctx, db.CompleteWorkflowTeardownOperationParams{
		UpdatedAt: db.FormatTime(time.Now().UTC()), ID: operationID,
	})
}

func (s *SQLStore) FailTeardown(ctx context.Context, operationID, detail string) error {
	return s.queries.FailWorkflowTeardownOperation(ctx, db.FailWorkflowTeardownOperationParams{
		LastError: detail, UpdatedAt: db.FormatTime(time.Now().UTC()), ID: operationID,
	})
}
