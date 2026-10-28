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
// Revision5 is the known native-authority intermediate; recognizing it here
// does not register a new shipped baseline.
func ProgramReceipts(target Baseline) Step {
	source := append([]byte{}, programReceiptsSQL...)
	source = append(source, linkKindImplementation...)
	source = append(source, linkTableRebuildSQL...)
	return Step{ID: "portable-program-receipts", Source: source, RebuildTables: true,
		Checksum: "ce7a4c16c8f72b3a687240958c39d88d5c0449018d8ade052382474791690a8f",

		From: Baseline{Revision: 5, Shape: "9916a026f07a01278311170fe3c896b12cbbd7569d6f11345870764806f8167b"},
		To:   target, Apply: func(ctx context.Context, tx *sql.Tx) error {
			if err := migrateLinkKind(ctx, tx); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, string(programReceiptsSQL))
			return err
		}}
}
