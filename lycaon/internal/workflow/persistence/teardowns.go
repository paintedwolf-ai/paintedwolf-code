package persistence

import (
	"context"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"time"
)

type Teardowns struct {
	transactions *Transactions
}

func (s *Teardowns) PendingTeardowns(ctx context.Context) ([]runstate.TeardownIntent, error) {
	rows, err := s.transactions.queries.ListPendingWorkflowTeardownOperations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]runstate.TeardownIntent, 0, len(rows))
	for _, row := range rows {
		out = append(out, runstate.TeardownIntent{
			ID: row.ID, RunID: row.RunID, SourceRevision: row.SourceRevision,
			CancelScope: runstate.WorkerCancelScope(row.CancelScope), AbortDelegation: row.AbortDelegation == 1,
			Reason: row.Reason,
		})
	}
	return out, nil
}

func (s *Teardowns) CompleteTeardown(ctx context.Context, operationID string) error {
	return s.transactions.queries.CompleteWorkflowTeardownOperation(ctx, db.CompleteWorkflowTeardownOperationParams{
		UpdatedAt: db.FormatTime(time.Now().UTC()), ID: operationID,
	})
}

func (s *Teardowns) FailTeardown(ctx context.Context, operationID, detail string) error {
	return s.transactions.queries.FailWorkflowTeardownOperation(ctx, db.FailWorkflowTeardownOperationParams{
		LastError: detail, UpdatedAt: db.FormatTime(time.Now().UTC()), ID: operationID,
	})
}
