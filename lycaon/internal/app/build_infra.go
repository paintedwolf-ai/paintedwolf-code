package app

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/api/capabilityadmin"
	"github.com/lycaon/lycaon/internal/app/deviceidentity"
	"github.com/lycaon/lycaon/internal/app/eventing"
	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/lycaon/lycaon/internal/worker"
	"os"
)

func (b *serveBuilder) wireToolRuntime() error {
	if err := b.decisions.Load(b.startup.cfg.TestDecider); err != nil {
		return err
	}
	var err error
	b.turnLoads = turnload.NewLedger()
	b.toolRuntime, err = loadToolRuntime(b.settings.Service, b.catalog.ModuleRoot, b.catalog.Effective, b.turnLoads, b.decisions.Rerank)
	if err != nil {
		return fmt.Errorf("tool runtime: %w", err)
	}
	b.providers.BindCurator()
	b.toolRuntime.Survey.SetReadEvidenceLedger(b.storage.Sessions)
	b.security.Detections.Load(b.storage.Directory, b.catalog.DeviceView.DetectionPacks(), b.toolRuntime.Authority)
	hintCfg, rejectFmt, err := loadStockHintRegistry()
	if err != nil {
		return fmt.Errorf("hint registry: %w", err)
	}
	b.hintCfg = hintCfg
	b.rejectFmt = rejectFmt
	if b.rejectFmt != nil {
		b.toolRuntime.Authority.ApplyGuidanceRejects(b.rejectFmt)
	}
	schemaCfg, err := loadToolSchemas()
	if err != nil {
		return fmt.Errorf("tool schemas: %w", err)
	}
	b.toolRuntime.Executor.Metadata.SetToolSchemas(schemaCfg)
	b.toolReg = tools.NewExecutorRegistry(b.toolRuntime.Executor, b.toolRuntime.Registry)
	return nil
}

func (b *serveBuilder) wireEvents() error {
	b.settings.BuildHostPower(b.startup.resources)
	eventRuntime, err := eventing.Build(b.startup.ctx, b.storage.Database, b.storage.Sessions, b.storage.Projects, b.settings.Power, b.mgr, b.mgr, b.mgr, b.startup.resources)
	if err != nil {
		return err
	}
	b.events = eventRuntime
	b.mgr.OARPipeline().SetEventPublisher(oarHostEventPublisher{publisher: eventRuntime.Publisher})
	b.mgr.SetAgentPresence(eventRuntime.Agents)
	publisher, vaultContext := eventRuntime.Publisher, b.startup.ctx
	b.security.BindVaultPublisher(func(chat string, unlocks *presence.Unlocks) {
		publisher.PublishChatVault(vaultContext, capabilityadmin.ChatVaultState(chat, unlocks))
	})
	if err := (delegationWiring{b}).registerRecovery(bootrecovery.Entry{
		Name: "rewind-operations", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
		After: []string{"tool-invocations", "source-mutations", "editor-documents"},
		Run:   b.mgr.RecoverRewinds,
	}); err != nil {
		return err
	}
	if err := (sessionWiring{b}).wireCheckpointRuntime(); err != nil {
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
	b.workersCfg, err = worker.LoadWorkersConfig()
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
