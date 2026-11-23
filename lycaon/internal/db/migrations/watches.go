package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed watches.sql
var watchesSQL []byte

// Watches adds watch registration, match, delivery and lease-selection storage.
func Watches() Part {
	return Part{
		Source: watchesSQL,
		Writes: func(context.Context, DBTX, uint64) (uint64, error) {
			// Empty tables, indexes and schema rows fit thirty-two maximum-sized pages.
			return 32 * 65536, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(watchesSQL))
			return err
		},
	}
}
