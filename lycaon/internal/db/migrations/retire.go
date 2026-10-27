package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
)

// Retirement is a released table, or one released column, that a step drops.
// Capture selects the released rows; Rebuild selects the same columns from
// what the upgraded store keeps, or from the table the rows moved to.
type Retirement struct {
	Table, Column    string
	Capture, Rebuild string
}

// retirements lists, by step ID, every released table or column a step drops
// through DropReleased. The contract suite upgrades the released corpus
// fixtures that hold these rows and requires Rebuild to reproduce Capture.
var retirements = map[string][]Retirement{}

// RegisteredRetirements returns a copy of the registered retirements by step ID.
func RegisteredRetirements() map[string][]Retirement {
	out := make(map[string][]Retirement, len(retirements))
	for step, items := range retirements {
		out[step] = slices.Clone(items)
	}
	return out
}

// DropReleased is the only way a step drops a table or column that exists in a
// released baseline. It refuses a retirement the step has not registered.
func DropReleased(ctx context.Context, tx *sql.Tx, step string, r Retirement) error {
	if !slices.Contains(retirements[step], r) {
		return fmt.Errorf("migration %s drops %s %s without a registered retirement", step, r.Table, r.Column)
	}
	statement := fmt.Sprintf(`DROP TABLE %q`, r.Table)
	if r.Column != "" {
		statement = fmt.Sprintf(`ALTER TABLE %q DROP COLUMN %q`, r.Table, r.Column)
	}
	_, err := tx.ExecContext(ctx, statement)
	return err
}
