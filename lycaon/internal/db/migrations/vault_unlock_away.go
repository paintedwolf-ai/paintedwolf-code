package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed vault_unlock_away.sql
var vaultUnlockAwayMigration []byte

// vaultUnlockAwaySource is the candidate revision-5 shape.
var vaultUnlockAwaySource = Baseline{Revision: 5, Shape: "75a4630362bee47170cf4c307b98d66630569ce3cbad397f71cd920e1efd2d54"}

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
