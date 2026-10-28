package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed program_receipts.sql
var programReceiptsSQL []byte

// ProgramReceipts adds host execution records without manufacturing model calls.
// Revision7 is the known native-authority intermediate; recognizing it here
// does not register a new shipped baseline.
func ProgramReceipts(target Baseline) Step {
	return Step{ID: "portable-program-receipts", Source: programReceiptsSQL,
		Checksum: "c18b38d5f7b1fc0d87fed72bfa54514939ce97cb6e8642ad1bdfd40db74c3213",
		From:     Baseline{Revision: 7, Shape: "3f850531058aa188145c4ff350d0d68bfa8810d2d3986070b34cd7d549489956"},
		To:       target, Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(programReceiptsSQL))
			return err
		}}
}
