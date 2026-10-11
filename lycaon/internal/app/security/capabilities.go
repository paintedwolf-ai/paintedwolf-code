package security

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

func (b *Runtime) BuildCapabilities(registry *tools.DefaultRegistry, authority interface{ SetSecretResolver(*secretcap.Service) }) error {
	if b.Capabilities != nil {
		return nil
	}
	remember := func(rootSessionID string, values []secretmatch.Remembered) {
		if b.Harvest != nil {
			b.Harvest.Remember(rootSessionID, values...)
		}
	}
	service, err := secretcap.New(b.database, remember)
	if err != nil {
		return fmt.Errorf("secret capability store: %w", err)
	}
	service.SetPresence(b.Presence)
	service.SetUnlocks(b.Unlocks)
	service.SetFingerprinter(b.Fingerprinter)
	// Unlocks live in memory, so none survived the last engine.
	if err := service.CloseUnlocksLeftOpen(b.ctx); err != nil {
		return err
	}
	if err := service.Reconcile(b.ctx); err != nil {
		return fmt.Errorf("reconcile secret capabilities: %w", err)
	}
	if err := native.RegisterSecretCapabilityTools(registry, service); err != nil {
		return fmt.Errorf("secret capability tools: %w", err)
	}
	authority.SetSecretResolver(service)
	b.Capabilities = service
	return nil
}
