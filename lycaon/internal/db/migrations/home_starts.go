package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed home_starts.sql
var homeStartsSQL []byte

// HomeStarts records each Home submission's reserved project, chat and first prompt.
func HomeStarts() Part {
	return Part{Source: homeStartsSQL, Writes: func(context.Context, DBTX, uint64) (uint64, error) {
		// Two empty tables, their indexes and two triggers fit in twelve maximum-sized pages.
		return 12 * 65536, nil
	}, Apply: func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, string(homeStartsSQL))
		return err
	}}
}
