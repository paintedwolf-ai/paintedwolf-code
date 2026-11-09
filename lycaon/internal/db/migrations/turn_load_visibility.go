package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed turn_load_visibility.sql
var turnLoadVisibilitySQL []byte

// TurnLoadVisibility preserves receipts while admitting continuation visibility changes.
func TurnLoadVisibility() Part {
	return Part{Source: turnLoadVisibilitySQL, RebuildTables: true, Apply: func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, string(turnLoadVisibilitySQL))
		return err
	}}
}
