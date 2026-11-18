package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed workflow_manifest_storage.sql
var workflowManifestStorageSQL []byte

func WorkflowManifestStorage(target Baseline) Step {
	return Step{
		ID: "workflow-manifest-storage", From: Baseline{Revision: 5, Shape: "a1c252d46e94940750d9b239e96716d7f8d3a612fb1a2d24969085876aac2aaa"}, To: target,
		Source: workflowManifestStorageSQL, Checksum: "37a3dfc897a09d0bdc94a5d25829ed8b1d1630658fc47e419add561a3e370da6",
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(workflowManifestStorageSQL))
			return err
		},
		// Both schema changes leave existing records in place.
		Writes: func(context.Context, DBTX, uint64) (uint64, error) { return 0, nil },
	}
}
