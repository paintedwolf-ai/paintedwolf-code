package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed vault_unlock_away.sql
var vaultUnlockAwayMigration []byte

// vaultUnlockAwaySource is the shipped revision-4 shape.
var vaultUnlockAwaySource = Baseline{Revision: 4, Shape: "c0280798481d97c01e3ddf30812db8b6c28db69520cd8b33b88642ddb03d18a2"}

// VaultUnlockAway admits an unlock that ended because the person's input went
// idle for the step-away period to the unlock audit.
func VaultUnlockAway(target Baseline) Step {
	return Step{
		ID:       "vault-unlock-away",
		Source:   vaultUnlockAwayMigration,
		Checksum: "13094df267f14b2a7cda143ef5ac6466bc242fb65f5f5452c6e3554e285d2676",
		From:     vaultUnlockAwaySource,
		To:       target,
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(vaultUnlockAwayMigration))
			return err
		},
		// The rebuilt audit table stays in the main SQLite database and uses its work reservation.
		ScratchBytes: 0,
	}
}
