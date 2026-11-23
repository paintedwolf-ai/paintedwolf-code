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
			return 1 << 20, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(watchesSQL))
			return err
		},
	}
}
