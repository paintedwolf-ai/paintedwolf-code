package db

import (
	"context"
	"database/sql"
	"errors"
)

var errPageInsertRequiresTx = errors.New("pageable insert requires a transaction")

// InsertWorkflowRun inserts the run and its pagination ordinal.
func (q *Queries) InsertWorkflowRun(ctx context.Context, arg InsertWorkflowRunRowParams) error {
	if _, ok := q.db.(*sql.Tx); !ok {
		return errPageInsertRequiresTx
	}
	if err := q.InsertWorkflowRunRow(ctx, arg); err != nil {
		return err
	}
	return q.InsertWorkflowRunPageOrdinal(ctx, arg.ID)
}

// InsertCodeScan inserts the scan and its pagination ordinal.
func (q *Queries) InsertCodeScan(ctx context.Context, arg InsertCodeScanRowParams) error {
	if _, ok := q.db.(*sql.Tx); !ok {
		return errPageInsertRequiresTx
	}
	if err := q.InsertCodeScanRow(ctx, arg); err != nil {
		return err
	}
	return q.InsertCodeScanPageOrdinal(ctx, arg.ID)
}
