package db

import (
	"context"
	"database/sql"

	"github.com/lycaon/lycaon/internal/db/migrations"
)

func reviewAssignmentsStep(target migrations.Baseline) migrations.Step {
	return migrations.Step{
		ID: "review-assignments-v2", Source: reviewAssignmentsMigration,
		Checksum: "425c79cbae0f4cfed8183b63a5c527e5bd6629716cab4ce17e303111fc3e7964",
		From:     migrations.Baseline{Revision: 1, Shape: "4c183bce0a7e107596c03a44a68b384cf216b3932ddf31e17d95f400772f478a"},
		To:       target,
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(reviewAssignmentsMigration))
			return err
		},
	}
}
