package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed message_editor_context.sql
var messageEditorContextSQL []byte

// MessageEditorContext records what a prompt's sender had open in the editor.
func MessageEditorContext() Part {
	return Part{Source: messageEditorContextSQL, Apply: func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, string(messageEditorContextSQL))
		return err
	}}
}
