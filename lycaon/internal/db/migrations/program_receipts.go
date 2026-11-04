package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed program_receipts.sql
var programReceiptsSQL []byte

// ProgramReceipts adds host execution records without manufacturing model calls.
func ProgramReceipts() Part {
	return Part{Source: programReceiptsSQL, Apply: func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, string(programReceiptsSQL))
		return err
	}}
}
