package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed version_reclaim_probe.sql
var versionReclaimProbeMigration []byte

// VersionReclaimProbe makes the deleted-version blob probe use its index, so
// deleting a project's versions does work linear in them.
func VersionReclaimProbe() Part {
	return Part{Source: versionReclaimProbeMigration,
		Writes: func(context.Context, DBTX, uint64) (uint64, error) { return 0, nil },
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(versionReclaimProbeMigration))
			return err
		}}
}
