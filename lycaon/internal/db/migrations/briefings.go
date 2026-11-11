package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed briefings.sql
var briefingsSQL []byte

// Briefings adds content-addressed briefing results, records and targets.
func Briefings() Part {
	return Part{Source: briefingsSQL, Apply: func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, string(briefingsSQL))
		return err
	}}
}
