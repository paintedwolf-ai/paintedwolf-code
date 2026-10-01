package db

import (
	"context"
	"database/sql"
)

// InTx commits a mutation and its event before notifying delivery.
func InTx(
	ctx context.Context,
	sqlDB Handle,
	queries *Queries,
	notify func(),
	fn func(qtx *Queries, tx *sql.Tx) error,
) error {
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(queries.WithTx(tx), tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if notify != nil {
		notify()
	}
	return nil
}
