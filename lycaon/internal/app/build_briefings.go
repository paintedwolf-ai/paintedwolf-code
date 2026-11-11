package app

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/activitybriefing"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/briefingstore"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/stepbriefing"
)

// wireFileBriefings applies the briefing retention policy and builds the service.
func wireFileBriefings(b *serveBuilder, deps *api.Dependencies) error {
	fileBriefingConfig, err := filebriefing.LoadConfig()
	if err != nil {
		return fmt.Errorf("file briefing config: %w", err)
	}
	fileBriefingStore := filebriefing.NewSQL(b.storage.Database)
	if b.settings.Service != nil && b.settings.Service.FileSummaries != nil && !b.settings.Service.FileSummaries.Enabled() {
		if err := fileBriefingStore.Clear(b.startup.ctx); err != nil {
			return fmt.Errorf("clear disabled file briefings: %w", err)
		}
	} else if err := fileBriefingStore.MaintainDevice(b.startup.ctx, fileBriefingConfig.Retention); err != nil {
		return fmt.Errorf("maintain file briefings: %w", err)
	}
	deps.Source.Briefings.Files = filebriefing.NewService(b.startup.ctx, filebriefing.Dependencies{
		Store: fileBriefingStore, Config: fileBriefingConfig, Generator: filebriefing.NewModelGenerator(b.providers.Service, b.sessions.Manager.Coordinator.Model.Cost),
		Events: b.events.Hub, Settings: b.settings.Service.FileSummaries, Logger: b.startup.logger, Scheduler: b.providers.Briefings.Scheduler, Records: b.providers.Briefings.Store,
	})
	deps.Source.Briefings.Host = b.providers.Briefings
	b.providers.Briefings.Publish(b.events.Hub, b.startup.logger)
	return registerBriefingProfiles(b)
}

// registerBriefingProfiles makes each profile the briefing endpoint serves
// available on the shared engine.
func registerBriefingProfiles(b *serveBuilder) error {
	programs, ok := b.sessions.Invocations.(activitybriefing.Programs)
	if !ok {
		return fmt.Errorf("tool activity briefings need program activity reads")
	}
	cause := briefingstore.NewCausal(b.storage.Database)
	if err := b.providers.Briefings.Registry.Register(activitybriefing.New(b.storage.Sessions, programs, cause)); err != nil {
		return err
	}
	if b.storage.SourceLedger == nil {
		return nil
	}
	return b.providers.Briefings.Registry.Register(stepbriefing.New(stepbriefing.LedgerOf(b.storage.SourceLedger), cause))
}
