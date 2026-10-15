package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed presence_authenticators.sql
var presenceAuthenticatorsMigration []byte

// PresenceAuthenticators admits the Linux authenticators to the reveal and unlock audits.
func PresenceAuthenticators(target Baseline) Step {
	return Step{
		ID:       "linux-presence-authenticators",
		Source:   presenceAuthenticatorsMigration,
		Checksum: "f001792bcbcdff35dbd392d0934adafb9b493554cd59cbae00f70013b09a763d",
		From:     sourceNamespaceTarget,
		To:       target,
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(presenceAuthenticatorsMigration))
			return err
		},
		// The rebuilt audit tables stay in the main SQLite database and use its work reservation.
		ScratchBytes: 0,
	}
}
