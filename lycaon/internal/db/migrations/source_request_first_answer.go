package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed source_request_first_answer.sql
var sourceRequestFirstAnswerSQL []byte

// SourceRequestFirstAnswer records the first transport response kind on each
// journaled source file request so retries match it across restarts.
func SourceRequestFirstAnswer() Part {
	return Part{
		Source: sourceRequestFirstAnswerSQL,
		Writes: sourceRequestFirstAnswerWrites,
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(sourceRequestFirstAnswerSQL))
			return err
		},
	}
}

func sourceRequestFirstAnswerWrites(ctx context.Context, store DBTX, _ uint64) (uint64, error) {
	var bytes uint64
	if err := store.QueryRowContext(ctx, `SELECT COALESCE(SUM(pgsize), 0) FROM dbstat WHERE name='source_file_requests'`).Scan(&bytes); err != nil {
		return 0, err
	}
	return storageSum(bytes, bytes, 1<<20)
}
