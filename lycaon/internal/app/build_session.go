package app

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/app/sessions"
)

// wireSessionManager wires the session manager, its authorization, and secret handling.
func (b *serveBuilder) wireSessionManager() error {
	s, err := sessions.Build(b.startup.ctx, sessions.Dependencies{
		Storage:           b.storage,
		Catalog:           b.catalog,
		Providers:         b.providers,
		Execution:         b.execution,
		Security:          b.security,
		Settings:          b.settings,
		Agents:            b.agents,
		TestSessionLimits: b.startup.cfg.TestSessionLimits,
		RegisterRecovery:  b.registerRecovery,
	})
	if err != nil {
		return err
	}
	b.sessions = s
	return nil
}

func (b *serveBuilder) wireCheckpointRuntime() error {
	return b.sessions.WireCheckpoints(b.startup.ctx, sessions.CheckpointDependencies{
		Database:        b.storage.Database,
		Directory:       b.storage.Directory,
		Sessions:        b.storage.Sessions,
		EventsOutbox:    b.events.Outbox,
		EventPublisher:  b.events.Publisher,
		Security:        b.security,
		Execution:       b.execution,
		SettingsService: b.settings.Service,
		Resources:       b.startup.resources,
	})
}

func (b *serveBuilder) assertAuthzCapturer() error {
	if b.storage.Database == nil {
		return nil
	}
	if b.security.Authority == nil {
		return fmt.Errorf("authz: capturer must be wired when database is configured")
	}
	if b.security.Authority.Store == nil || b.security.Authority.Sealer == nil || b.security.Authority.Ledger == nil {
		return fmt.Errorf("authz: capturer incomplete")
	}
	if b.sessions == nil || b.sessions.Manager == nil || !b.sessions.Manager.Runner.Authorization.Wired() {
		return fmt.Errorf("authz: session manager missing authz sealer")
	}
	return nil
}
