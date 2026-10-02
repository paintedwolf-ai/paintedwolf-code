package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/version"
)

// wirePresence installs presence verification and the vault release ledger.
// A key from an unverified launcher leaves presence unavailable rather than
// failing boot: everything else works, and held values stay in the vault.
func (b *serveBuilder) wirePresence() error {
	broker := presence.NewBroker()
	key, err := presence.TrustedKey(os.Getenv(presence.PublicKeyEnv), credentialstore.UnlocksUnattended(),
		func() error { return presence.VerifyLauncher(version.BundleID) })
	if err != nil {
		slog.Warn("presence verification is unavailable", "component", "presence", "error", err)
	}
	if err := broker.Configure(key); err != nil {
		return fmt.Errorf("configure presence verification: %w", err)
	}
	ledger, err := presence.OpenReleaseLedger()
	if err != nil {
		return fmt.Errorf("presence release ledger: %w", err)
	}
	b.presenceBroker, b.releaseLedger = broker, ledger
	b.toolRuntime.SetReleaseLedger(ledger)
	return nil
}

// heldReleaseRecorder writes an attested release's audit rows in the
// approval's commit.
type heldReleaseRecorder struct{}

func (heldReleaseRecorder) RecordReleaseTx(ctx context.Context, tx *sql.Tx, release hitl.AttestedRelease) error {
	held := make([]secretcap.HeldValue, 0, len(release.Held.Secrets))
	for _, secret := range release.Held.Secrets {
		held = append(held, secretcap.HeldValue{SecretID: secret.SecretID, Version: secret.Version, Name: secret.Name})
	}
	return secretcap.RecordRelease(ctx, tx, secretcap.ReleaseRecord{
		AttestationID: release.Attestation.ID, CheckpointID: release.CheckpointID,
		Scope: secretcap.ReleaseScope(release.Scope), Recipients: release.Recipients, Held: held,
		Authenticator: release.Attestation.Authenticator, WindowLabel: release.WindowLabel,
		PersonID: release.Attestation.PersonID, AttestedAt: release.Attestation.AttestedAt,
	})
}
