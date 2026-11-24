package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed completion_cues.sql
var completionCuesSQL []byte

// CompletionCues adds durable per-intent cue delivery state for coordinators.
func CompletionCues() Part {
	return Part{
		Source: completionCuesSQL,
		Writes: func(context.Context, DBTX, uint64) (uint64, error) { return 4096, nil },
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(completionCuesSQL))
			return err
		},
	}
}
