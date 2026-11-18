package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed command_window_coverage.sql
var commandWindowCoverageSQL []byte

// commandWindowCoverageWrites bounds creating one empty table.
const commandWindowCoverageWrites = 1 << 20

// CommandWindowCoverage records how much of each folder a command window compared.
func CommandWindowCoverage() Part {
	return Part{
		Source: commandWindowCoverageSQL,
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(commandWindowCoverageSQL))
			return err
		},
		Writes: func(context.Context, DBTX, uint64) (uint64, error) { return commandWindowCoverageWrites, nil },
	}
}
