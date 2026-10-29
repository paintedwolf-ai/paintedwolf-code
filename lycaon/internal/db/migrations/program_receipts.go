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
		Checksum: "bde8b3115696ead32e21b55f520fff50c94e04da79604a0a71a6e7889370ef40",
		From:     Baseline{Revision: 7, Shape: "3f850531058aa188145c4ff350d0d68bfa8810d2d3986070b34cd7d549489956"},
		To:       target, Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(programReceiptsSQL))
			return err
		}}
}
