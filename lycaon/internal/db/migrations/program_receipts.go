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

//go:embed link_table_rebuild.sql
var linkTableRebuildSQL []byte

// ProgramReceipts adds host execution records without manufacturing model calls.
// Revision7 is the known native-authority intermediate; recognizing it here
// does not register a new shipped baseline.
func ProgramReceipts(target Baseline) Step {
	source := append([]byte{}, programReceiptsSQL...)
	source = append(source, linkKindImplementation...)
	source = append(source, linkTableRebuildSQL...)
	return Step{ID: "portable-program-receipts", Source: source, RebuildTables: true,
		Checksum: "7abc1d0ca764d4c8e2220811657f9fabb0e99fe0de3fcf1f134f5b05c87dae14",
		From:     Baseline{Revision: 7, Shape: "3f850531058aa188145c4ff350d0d68bfa8810d2d3986070b34cd7d549489956"},
		To:       target, Apply: func(ctx context.Context, tx *sql.Tx) error {
			if err := migrateLinkKind(ctx, tx); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, string(programReceiptsSQL))
			return err
		}}
}
