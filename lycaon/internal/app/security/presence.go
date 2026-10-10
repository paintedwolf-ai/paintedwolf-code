package security

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/version"
)

func (b *Runtime) BuildPresence(authority interface{ SetVaultUnlocks(*presence.Unlocks) }) error {
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
			if b.Capabilities != nil {
				b.Capabilities.RecordUnlockEnd(b.ctx, unlock, reason, at)
			}
		},
		Changed: func(chatSessionID string) {
			if b.publishVault != nil {
				b.publishVault(chatSessionID, unlocks)
			}
		},
	})
	b.Presence, b.Unlocks = broker, unlocks
	authority.SetVaultUnlocks(unlocks)
	return nil
}

func (b *Runtime) BindVaultPublisher(publish func(string, *presence.Unlocks)) {
	b.publishVault = publish
}
