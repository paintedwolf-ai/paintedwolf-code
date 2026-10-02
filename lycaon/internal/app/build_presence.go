package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/lycaon/lycaon/internal/api/capabilityadmin"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/version"
)

// wirePresence installs presence verification and the chats' unlocks.
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
	unlocks := presence.NewUnlocks()
	// The audit and event sinks are built later; each change reads them then.
	unlocks.SetObserver(presence.UnlockObserver{
		Ended: func(unlock presence.Unlock, reason presence.EndReason, at time.Time) {
			if b.secretCaps != nil {
				b.secretCaps.RecordUnlockEnd(b.ctx, unlock, reason, at)
			}
		},
		Changed: func(chatSessionID string) {
			b.eventPub.PublishChatVault(b.ctx, capabilityadmin.ChatVaultState(chatSessionID, unlocks))
		},
	})
	b.presenceBroker, b.vaultUnlocks = broker, unlocks
	b.toolRuntime.Executor.SetVaultUnlocks(unlocks)
	return nil
}

// unlockRecorder audits an unlock in the commit of the approval that
// opened it.
type unlockRecorder struct{}

func (unlockRecorder) RecordUnlockTx(ctx context.Context, tx *sql.Tx, projectID string, unlock presence.Unlock) error {
	return secretcap.RecordUnlock(ctx, tx, projectID, unlock)
}
