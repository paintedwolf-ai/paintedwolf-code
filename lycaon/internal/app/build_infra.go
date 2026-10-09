package app

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/api/capabilityadmin"
	"github.com/lycaon/lycaon/internal/app/deviceidentity"
	"github.com/lycaon/lycaon/internal/app/eventing"
	"github.com/lycaon/lycaon/internal/app/execution"
	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/lycaon/lycaon/internal/worker"
	"os"
)

func (b *serveBuilder) wireToolRuntime() error {
	if err := b.decisions.Load(b.startup.cfg.TestDecider); err != nil {
		return err
	}
	var err error
	b.execution, err = execution.Build(b.settings.Service, b.catalog.ModuleRoot, b.catalog.Effective, b.decisions.Rerank)
	if err != nil {
		return err
	}
	b.providers.BindCurator()
	b.execution.Host.Survey.SetReadEvidenceLedger(b.storage.Sessions)
	b.security.Detections.Load(b.storage.Directory, b.catalog.DeviceView.DetectionPacks(), b.execution.Host.Authority)
	return b.execution.LoadGuidance()
}

func (b *serveBuilder) wireEvents() error {
	b.settings.BuildHostPower(b.startup.resources)
	eventRuntime, err := eventing.Build(b.startup.ctx, b.storage.Database, b.storage.Sessions, b.storage.Projects, b.settings.Power, b.sessions.Manager, b.sessions.Manager, b.sessions.Manager, b.startup.resources)
	if err != nil {
		return err
	}
	b.events = eventRuntime
	b.sessions.Manager.OARPipeline().SetEventPublisher(oarHostEventPublisher{publisher: eventRuntime.Publisher})
	b.sessions.Manager.SetAgentPresence(eventRuntime.Agents)
	publisher, vaultContext := eventRuntime.Publisher, b.startup.ctx
	b.security.BindVaultPublisher(func(chat string, unlocks *presence.Unlocks) {
		publisher.PublishChatVault(vaultContext, capabilityadmin.ChatVaultState(chat, unlocks))
	})
	if err := b.registerRecovery(bootrecovery.Entry{
		Name: "rewind-operations", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
		After: []string{"tool-invocations", "source-mutations", "editor-documents"},
		Run:   b.sessions.Manager.RecoverRewinds,
	}); err != nil {
		return err
	}
	if err := b.wireCheckpointRuntime(); err != nil {
		return err
	}

	project.SetDefaultOpenPolicy(project.DefaultOpenPolicy())

	b.identity, err = deviceidentity.Load()
	if err != nil {
		return err
	}

	logFields := []any{"level", os.Getenv("LYCAON_LOG_LEVEL")}
	if path := observability.ActiveLogFilePath(); path != "" {
		logFields = append(logFields, "file", path)
	}
	b.startup.logger.Info("logging configured", logFields...)
	b.worker.cfg, err = worker.LoadWorkersConfig()
	if err != nil {
		return fmt.Errorf("workers config: %w", err)
	}
	return nil
}

func (b *serveBuilder) loadConfig() error {
	if _, err := tsparse.LoadConfig(); err != nil {
		return fmt.Errorf("source parsing: %w", err)
	}
	if err := b.settings.Load(b.startup.cfg); err != nil {
		return err
	}
	b.security.BindTrust(b.settings.Service.TrustSurfaces)
	return b.catalog.Load(b.startup.ctx, b.startup.logger)
}
