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
		Checksum: "96ea279dd0369c35a3c7f14ad9d047042e86756eec0644ad0a65295b3e472b03",
		From:     Baseline{Revision: 5, Shape: "9916a026f07a01278311170fe3c896b12cbbd7569d6f11345870764806f8167b"},
		To:       target, Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(programReceiptsSQL))
			return err
		}}
}
