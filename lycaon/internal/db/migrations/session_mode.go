package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed session_mode.sql
var sessionModeSQL []byte

// SessionMode assigns durable root authority to each chat tree.
func SessionMode() Part {
	return Part{
		Source: sessionModeSQL, RebuildTables: true,
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(sessionModeSQL))
			return err
		},
	}
}
