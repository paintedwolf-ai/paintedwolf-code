package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

// revision5Release is the revision-5 shape the 1.0.3 release candidates ship.
var revision5Release = Baseline{Revision: 5, Shape: "bce201a54977c0bef851540d14bad604ffa672203b9837d757ace411fe7e2af6"}

// Revision5Release names the shipped revision-5 shape for the step that ends there.
func Revision5Release() Baseline { return revision5Release }

// Revision6 is the step from the shipped revision-5 shape. Until a build
// holding revision 6 is published, release candidates included, schema changes
// join these parts instead of advancing SchemaVersion.
func Revision6(target Baseline) Step {
	return composeStep(Step{
		ID:       "outside-namespace",
		Checksum: "a2ab0efd240f7ddb5385626c04ea66b9b9ad859b9af90ebcdbbe6b479ec7386e",
		From:     revision5Release, To: target,
	}, OutsideNamespace(), CommandWindowCoverage())
}

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
