package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed vault_unlock_away.sql
var vaultUnlockAwayMigration []byte

// VaultUnlockAway admits an unlock that ended because the person's input went
// idle for the step-away period to the unlock audit.
func VaultUnlockAway() Part {
	return Part{Source: vaultUnlockAwayMigration, Apply: func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, string(vaultUnlockAwayMigration))
		return err
	}}
}
