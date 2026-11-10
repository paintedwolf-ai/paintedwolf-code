package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed turn_load_visibility.sql
var turnLoadVisibilitySQL []byte

//go:embed turn_load_visibility_publish.sql
var turnLoadVisibilityPublishSQL []byte

//go:embed turn_load_visibility.go
var turnLoadVisibilityImplementation []byte

var turnLoadReceiptRetirement = Retirement{
	Table:   "turn_load_receipts",
	Capture: "SELECT * FROM turn_load_receipts ORDER BY id",
	Rebuild: "SELECT * FROM turn_load_receipts ORDER BY id",
}

// TurnLoadVisibility preserves receipts while admitting continuation visibility changes.
func TurnLoadVisibility() Part {
	source := append(append(append([]byte(nil), turnLoadVisibilitySQL...), turnLoadVisibilityPublishSQL...), turnLoadVisibilityImplementation...)
	return Part{Source: source, RebuildTables: true, Apply: func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, string(turnLoadVisibilitySQL)); err != nil {
			return err
		}
		if err := DropReleased(ctx, tx, "source-manifests-and-command-records", turnLoadReceiptRetirement); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, string(turnLoadVisibilityPublishSQL))
		return err
	}}
}
