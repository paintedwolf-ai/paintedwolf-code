package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed message_view_context.sql
var messageViewContextSQL []byte

// MessageViewContext records what a prompt's sender had in view.
func MessageViewContext() Part {
	return Part{Source: messageViewContextSQL, Apply: func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, string(messageViewContextSQL))
		return err
	}}
}
