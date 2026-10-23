package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lycaon/lycaon/internal/db/migrations"
)

// Compare before intentional post-copy queue additions. Whole-table counts use
// primary rows independently of both the copy selection and secondary indexes.
func validateRebuildCounts(ctx context.Context, tx *sql.Tx, objects []rebuildObject, selection *migrations.RetainedCopy) error {
	for _, object := range objects {
		if object.kind != "table" || object.tableKind == "virtual" {
			continue
		}
		expected := selection.ExpectedRows[object.name]
		if expected == "" {
			expected = "SELECT COUNT(*) FROM original." + rebuildIdentifier(object.name) + " NOT INDEXED"
		}
		var source, copied int64
		if err := tx.QueryRowContext(ctx, expected).Scan(&source); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM main."+rebuildIdentifier(object.name)+" NOT INDEXED").Scan(&copied); err != nil {
			return err
		}
		if source != copied {
			return fmt.Errorf("retained table %s copied %d rows, expected %d", object.name, copied, source)
		}
	}
	return nil
}
