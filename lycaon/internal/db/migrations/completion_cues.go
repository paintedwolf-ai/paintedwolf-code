package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed completion_cues.sql
var completionCuesSQL []byte

// Revision6 adds durable per-intent completion cue state for coordinators.
func Revision6(from, target Baseline) Step {
	return Step{
		ID:       "coordinator-completion-cues",
		Checksum: "873ad25e6279cd188d3a8c8971cfa7cbc40d94979cd1bdaa03ac4a658cae9a06",
		Source:   completionCuesSQL,
		From:     from,
		To:       target,
		Writes:   func(context.Context, DBTX, uint64) (uint64, error) { return 4096, nil },
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(completionCuesSQL))
			return err
		},
	}
}
