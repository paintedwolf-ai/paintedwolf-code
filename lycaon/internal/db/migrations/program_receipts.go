package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed program_receipts.sql
var programReceiptsSQL []byte

//go:embed link_kind.go
var linkKindImplementation []byte

// ProgramReceipts adds host execution records without manufacturing model calls.
// Revision5 is the known native-authority intermediate; recognizing it here
// does not register a new shipped baseline.
func ProgramReceipts(target Baseline) Step {
	return Step{ID: "portable-program-receipts", Source: append(append([]byte{}, programReceiptsSQL...), linkKindImplementation...), RebuildTables: true,
		Checksum: "c312d2932281f53e6a156d2c72f779a307ab4e24bdedc184762624f5fc566085",
		From:     Baseline{Revision: 5, Shape: "9916a026f07a01278311170fe3c896b12cbbd7569d6f11345870764806f8167b"},
		To:       target, Apply: func(ctx context.Context, tx *sql.Tx) error {
			if err := migrateLinkKind(ctx, tx); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, string(programReceiptsSQL))
			return err
		}}
}
