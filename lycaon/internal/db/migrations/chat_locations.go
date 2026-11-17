package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed chat_locations.sql
var chatLocationsSQL []byte

// ChatLocations records where each root chat has worked outside the project.
func ChatLocations() Part {
	return Part{Source: chatLocationsSQL, Apply: func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, string(chatLocationsSQL))
		return err
	}}
}
