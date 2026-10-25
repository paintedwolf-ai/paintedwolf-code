package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed program_receipts.sql
var programReceiptsSQL []byte

// ProgramReceipts adds host execution records without manufacturing model calls.
// Revision5 is the known native-authority intermediate; recognizing it here
// does not register a new shipped baseline.
func ProgramReceipts(target Baseline) Step {
	return Step{ID: "portable-program-receipts", Source: programReceiptsSQL,
		Checksum: "8430dd153033400a1e73067b17c0d8345136fe2de086e15d290085fddeca3308",
		From:     Baseline{Revision: 5, Shape: "9916a026f07a01278311170fe3c896b12cbbd7569d6f11345870764806f8167b"},
		To:       target, Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(programReceiptsSQL))
			return err
		}}
}
