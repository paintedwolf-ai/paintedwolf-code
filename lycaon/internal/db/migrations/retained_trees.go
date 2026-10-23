package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed retained_trees.sql
var retainedTreesSQL []byte

// RetainedTrees makes the existing manifest representation explicit before
// enabling versioned retained-tree bindings. The SQL table shape is unchanged.
func RetainedTrees(target Baseline) Step {
	return Step{ID: "source-retained-tree-bindings", Source: retainedTreesSQL,
		Checksum: "23a9b4f0aef4bb348a96e6b114d5e0934ac4b7dee2ee31206677224506a4046d",
		From:     Baseline{Revision: 6, Shape: target.Shape}, To: target,
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(retainedTreesSQL))
			return err
		}}
}
