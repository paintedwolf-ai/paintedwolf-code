package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed program_receipts.sql
var programReceiptsSQL []byte

// ProgramReceipts adds host execution records without manufacturing model calls.
// Revision5 is a known authority-format intermediate with unchanged SQL shape;
// recognizing it here does not register a new shipped baseline.
func ProgramReceipts(target Baseline) Step {
	return Step{ID: "portable-program-receipts", Source: programReceiptsSQL,
		Checksum: "2bd2bcff3c3c240bd5c4b20319c59dc8c4eb525ce62e79393711db0e0a9bcc5b",
		From:     Baseline{Revision: 5, Shape: "c0280798481d97c01e3ddf30812db8b6c28db69520cd8b33b88642ddb03d18a2"},
		To:       target, Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(programReceiptsSQL))
			return err
		}}
}
