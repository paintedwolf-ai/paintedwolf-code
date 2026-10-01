package app

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/hostlock"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/pricing"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/startupprotocol"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/lycaon/lycaon/internal/version"
	"github.com/lycaon/lycaon/internal/webindex"
)

func (b *serveBuilder) initObservability() error {
	var err error
	b.logger = observability.NewServeLogger()
	slog.SetDefault(b.logger)

	b.addr, err = b.cfg.resolvedListenAddr()
	if err != nil {
		return err
	}
	return nil
}
func (b *serveBuilder) openStore() error {
	var err error
	dbPath, err := b.cfg.resolvedDBPath()
	if err != nil {
		return err
	}
	// Hold the store lease across restore, schema setup, and serving.
	claim, err := hostlock.AcquireStore(dbPath)
	if err != nil {
		return err
	}
	b.storeClaim = claim

	configDir := filepath.Dir(dbPath)
	if err := backup.CleanupInterruptedTransfers(configDir); err != nil {
		b.logger.Warn("could not clean interrupted backup transfers", "error", err)
	}
	if err := backup.ApplyPending(configDir); err != nil {
		return fmt.Errorf("apply staged restore before open: %w", err)
	}
	switch {
	case db.FreshEnabled():
		if err := localdata.ResetStoreCoupled(dbPath); err != nil {
			return fmt.Errorf("fresh development state: %w", err)
		}
		b.logger.Info("store-coupled development state wiped (LYCAON_DB_FRESH)", "path", dbPath)
	case db.FreshRequested():
		b.logger.Warn("LYCAON_DB_FRESH ignored on the production channel", "path", dbPath)
	}
	db.SetRunningAppVersion(version.Version)
	b.db, err = b.openUpgradeableStore(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	if err := claim.BindStore(); err != nil {
		return err
	}
	b.storePath = dbPath
	b.dataDir = filepath.Dir(dbPath)
	b.logger.Info("sqlite store", "path", dbPath)
	// Co-locate the cache with its store data root.
	indexPath := filepath.Join(filepath.Dir(dbPath), "web-index.db")
	if idx, ierr := webindex.Open(indexPath); ierr != nil {
		b.logger.Warn("web index unavailable", "path", indexPath, "error", ierr)
	} else {
		b.webIndex = idx
		b.logger.Info("web index", "path", indexPath)
	}
	b.retentionCfg = db.DefaultRetention()
	b.storeRevision, err = db.BumpStoreRevision(b.ctx, b.db)
	if err != nil {
		return fmt.Errorf("store revision: %w", err)
	}

	b.store = store.NewSQL(b.db)
	b.registry = project.NewSQLRegistry(b.db)
	wireAgentPolicyRoots(b.ctx, b.registry)
	b.sourceLedger = sourceledger.New(b.db, filepath.Join(b.dataDir, enginepaths.SourceContentDirName))
	b.sourceLedger.SetStoreGuard(b.storeClaim)
	b.sourceLedger.SetGitReader(gitStateReader{mgr: git.NewManager()})
	return nil
}
func (b *serveBuilder) loadConfig() error {
	if _, err := tsparse.LoadConfig(); err != nil {
		return fmt.Errorf("source parsing: %w", err)
	}
	var err error
	b.sessionCfg = settings.DefaultSessionLimits()
	if b.cfg.TestSessionLimits != nil {
		b.sessionCfg = settings.NormalizeSessionLimits(*b.cfg.TestSessionLimits)
	}

	b.settingsSvc, b.configRoot, err = loadSettingsService(b.cfg.ConfigRoot)
	if err != nil {
		return fmt.Errorf("settings service: %w", err)
	}
	if b.settingsSvc != nil && b.settingsSvc.Approvals != nil {
		// Confinement reads durable write-root grants live.
		confine.SetGrantedWriteRootsSource(b.settingsSvc.Approvals.WriteRootsForProject)
	}
	b.configRoot = configlayout.FindModuleRoot()
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		return fmt.Errorf("config dir: %w", err)
	}
	scanners := scan.RequirementChecker{ModuleRoot: b.configRoot, HomeDir: homeDir}
	b.viewCache = catalogview.NewCache(b.configRoot, b.logger)
	spawn.SetContributedAgentsSource(contributedWorkerAgents)
	eff, deviceView, err := extensionstate.PublishDeviceCatalog(b.ctx, b.viewCache,
		scanners, b.logger)
	if err != nil {
		return fmt.Errorf("extension packs: %w", err)
	}
	b.effective = eff
	b.deviceView = deviceView
	caps, err := promptattach.LoadCaps()
	if err != nil {
		return fmt.Errorf("prompt attachment caps: %w", err)
	}
	promptattach.Install(caps)
	providerwire.SetWireImageBound(caps.Transport.MaxImage)
	budgets, err := prompts.LoadPromptBudgets()
	if err != nil {
		return fmt.Errorf("prompt budgets: %w", err)
	}
	providerwire.SetPerceptionWindow(providerwire.PerceptionWindow{
		MaxToolImages: budgets.Perception.MaxToolImages,
		DropBatch:     budgets.Perception.DropBatch,
	})
	return nil
}
func (b *serveBuilder) wireLLM() error {
	var err error
	mockEnabled := llm.MockEnabled(b.cfg.TestLLMClient)

	if llm.ManualOnlyFromEnv() {
		// Manual harness completions arrive through /harness/llm.
		b.manualLLM = llm.NewManualProvider()
		b.mockLLM = b.manualLLM
	} else if mockEnabled {
		mockCfg, err := llm.LoadMockConfig()
		if err != nil {
			return fmt.Errorf("mock llm config: %w", err)
		}
		b.mockLLM = llm.NewMockProvider(mockCfg)
		if b.cfg.TestLLMClient != nil {
			b.mockLLM = b.cfg.TestLLMClient
		}
		b.mockLLM = llm.WrapLLMClientIfDebug(b.mockLLM, "mock")
	}

	b.llmSvc, err = llm.NewService(b.mockLLM)
	if err != nil {
		return fmt.Errorf("llm service: %w", err)
	}
	if !mockEnabled && (b.llmSvc == nil || b.llmSvc.Registry == nil || !b.llmSvc.Registry.AnyConfigured()) {
		slog.Warn("no LLM provider configured; prompts will fail until a provider API key is set")
	}
	if b.cfg.Startup != nil {
		if err := b.cfg.Startup.Phase(startupprotocol.PhasePricing); err != nil {
			return fmt.Errorf("startup protocol: %w", err)
		}
	}

	tracker := cost.NewSQLTracker(b.db, cost.NoopPricer{})
	if b.cfg.TestCostPricer != nil {
		tracker = cost.NewSQLTracker(b.db, b.cfg.TestCostPricer)
	}
	if err := b.registerRecovery(bootrecovery.Entry{
		Name: "llm-call-receipts", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
		Run: tracker.RecoverStartedCalls,
	}); err != nil {
		return err
	}
	b.costTracker = tracker

	if b.settingsSvc == nil || b.settingsSvc.Pricing == nil {
		return fmt.Errorf("pricing settings store not configured")
	}
	catalog, err := pricing.LoadSourcesConfig()
	if err != nil {
		return fmt.Errorf("pricing catalog: %w", err)
	}
	var live cost.LiveRateSource
	var kinds cost.KindResolver
	var modelFeed pricing.ModelFeed
	if b.llmSvc != nil && b.llmSvc.Registry != nil {
		live = b.llmSvc.Registry
		kinds = b.llmSvc.Registry
		modelFeed = b.llmSvc.Registry.ModelFeed()
	}
	host := &settings.PricingHost{
		Store:     b.settingsSvc.Pricing,
		Catalog:   catalog,
		CacheDir:  enginepaths.PricingCacheRootUnder(b.dataDir),
		Live:      live,
		Kinds:     kinds,
		ModelFeed: modelFeed,
		Tracker:   tracker,
	}
	if b.cfg.TestCostPricer == nil {
		if err := host.SyncFromStore(b.ctx); err != nil {
			return fmt.Errorf("pricing sync: %w", err)
		}
		host.StartAutoRefresh(b.ctx)
	}
	b.pricingHost = host
	return nil
}
func (b *serveBuilder) wireToolRuntime() error {
	if err := b.wireDecider(); err != nil {
		return err
	}
	var err error
	b.turnLoads = turnload.NewLedger()
	// The session manager does not exist yet; the resolver reaches it at call time.
	resolve := func(ctx context.Context, tctx tools.ToolContext, need string, cards []turnload.ToolCard) turnload.RequestOutcome {
		return b.mgr.ResolveToolRequest(ctx, tctx, need, cards)
	}
	record := func(ctx context.Context, tctx tools.ToolContext, outcome turnload.RequestOutcome, result turnload.RequestToolsResult, elapsed time.Duration) {
		b.mgr.RecordToolRequest(ctx, tctx, outcome, result, elapsed)
	}
	lookup := func(ctx context.Context, tctx tools.ToolContext, need string, roster []skills.Skill) turnload.LookupOutcome {
		return b.mgr.LookupSkills(ctx, tctx, need, roster)
	}
	b.toolRuntime, err = loadToolRuntime(b.settingsSvc, b.configRoot, b.effective, b.turnLoads, resolve, record, lookup, b.rerank)
	if err != nil {
		return fmt.Errorf("tool runtime: %w", err)
	}
	if b.llmSvc != nil && b.llmSvc.Registry != nil && b.llmSvc.Policy != nil && llm.ProviderUtilityCallsEnabled() {
		curator := b.llmSvc.BindSummarizer(&llm.RegistrySummarizer{
			Fallback: compaction.TruncateSummarizer{},
			Cost:     b.costTracker,
			Purpose:  "curate",
		})
		b.synthesisCurator = curator
	}
	b.toolRuntime.SetReadEvidenceLedger(b.store)
	b.wireDetectionPacks()
	hintCfg, rejectFmt, err := loadStockHintRegistry()
	if err != nil {
		return fmt.Errorf("hint registry: %w", err)
	}
	b.hintCfg = hintCfg
	b.rejectFmt = rejectFmt
	if b.rejectFmt != nil {
		b.toolRuntime.ApplyGuidanceRejects(b.rejectFmt)
	}
	schemaCfg, err := loadToolSchemas()
	if err != nil {
		return fmt.Errorf("tool schemas: %w", err)
	}
	b.toolRuntime.Executor.SetToolSchemas(schemaCfg)
	b.toolReg = tools.NewExecutorRegistry(b.toolRuntime.Executor, b.toolRuntime.Registry)
	return nil
}
func (b *serveBuilder) wireAgents() error {
	var err error
	b.agentRegistry = orchestration.NewMemoryAgentRegistry()
	if err := orchestration.LoadRequiredAgentRegistry(b.ctx, b.agentRegistry); err != nil {
		return fmt.Errorf("agent registry: %w", err)
	}
	if err := orchestration.ValidateGateAgents(b.agentRegistry); err != nil {
		return fmt.Errorf("agent registry gates: %w", err)
	}
	b.toolProfiles, err = sandbox.LoadToolProfiles()
	if err != nil {
		return fmt.Errorf("tool profiles: %w", err)
	}
	if err := orchestration.ValidateAgentToolProfiles(b.agentRegistry, b.toolProfiles); err != nil {
		return fmt.Errorf("agent tool profiles: %w", err)
	}
	b.postureRegistry, err = session.LoadPostureRegistry()
	if err != nil {
		return fmt.Errorf("posture registry: %w", err)
	}
	return nil
}

func (b *serveBuilder) llmRegistryPolicy() (*llm.Registry, *llm.PolicyStore) {
	if b.llmSvc == nil {
		return nil, nil
	}
	return b.llmSvc.Registry, b.llmSvc.Policy
}

func (b *serveBuilder) projectSettingsGate() *settings.ProjectSurfaceGate {
	return b.projectSurfaceGate(projectcontrib.SurfaceProjectSettings)
}

// projectScanConfigGate governs project scanner configuration.
func (b *serveBuilder) projectScanConfigGate() *settings.ProjectSurfaceGate {
	return b.projectSurfaceGate(projectcontrib.SurfaceScanConfig)
}

func (b *serveBuilder) projectMCPGate() *settings.ProjectSurfaceGate {
	return b.projectSurfaceGate(projectcontrib.SurfaceProjectMCP)
}

// projectSkillsGate covers both supported project skill roots.
func (b *serveBuilder) projectSkillsGate() *settings.ProjectSurfaceGate {
	return b.projectSurfaceGate(projectcontrib.SurfaceSkills)
}

// A nil project surface gate is closed.
func (b *serveBuilder) projectSurfaceGate(surface string) *settings.ProjectSurfaceGate {
	if b == nil || b.settingsSvc == nil || b.settingsSvc.TrustSurfaces == nil || b.registry == nil {
		return nil
	}
	return &settings.ProjectSurfaceGate{
		Surface:  surface,
		Surfaces: b.settingsSvc.TrustSurfaces,
		Projects: b.registry,
	}
}

// contributedWorkerAgents lists contributed dispatchable workers.
func contributedWorkerAgents() []string {
	eff := extpacks.Active()
	if eff == nil {
		return nil
	}
	profiles, err := agentdef.LoadEffectiveWithCatalog(eff)
	if err != nil {
		return nil
	}
	var out []string
	for _, profile := range profiles {
		if !slices.Contains(profile.TopologyRoles, agentdef.TopologyRoleWorker) {
			continue
		}
		unit, ok := eff.Loaded["agents/"+profile.ID]
		if !ok || eff.StockAuthority(unit.WinnerPackID) {
			continue
		}
		out = append(out, profile.ID)
	}
	return out
}

// wireAgentPolicyRoots protects the agent policy of every registered project,
// which other sessions load while this invocation is rooted elsewhere. A
// failed read keeps the last roots it saw.
func wireAgentPolicyRoots(ctx context.Context, registry *project.SQLRegistry) {
	var last atomic.Pointer[[]string]
	confine.SetAgentPolicyRootsSource(func() []string {
		paths, err := registry.RootPaths(context.WithoutCancel(ctx))
		if err != nil {
			if kept := last.Load(); kept != nil {
				return *kept
			}
			return nil
		}
		last.Store(&paths)
		return paths
	})
}

// wireDecider resolves the local decision engine and the rerank policies it
// carries into every ranking site; the decision-engine-warm runner loads the
// weights off the boot path, and an unresolved engine leaves every decision
// abstaining and every ranking site lexical.
func (b *serveBuilder) wireDecider() error {
	policies, err := decide.LoadPolicies()
	if err != nil {
		return fmt.Errorf("decision catalog: %w", err)
	}
	if b.cfg.TestDecider != nil {
		b.decider = b.cfg.TestDecider
	} else {
		cfg := bialy.ConfigFromEnvironment()
		if catalog, err := turnload.LoadCatalog(); err == nil {
			cfg.HeadMaxLen = catalog.State.HeadTokens
		}
		b.decider = bialy.New(cfg)
	}
	b.rerank = decide.Reranker{Decider: b.decider, Policies: policies}
	return nil
}

// deciderWarmer returns the engine's warm-up when it has one to run.
func (b *serveBuilder) deciderWarmer() (interface{ Warm(context.Context) error }, bool) {
	if b.decider == nil || !b.decider.Available() {
		return nil, false
	}
	warmer, ok := b.decider.(interface{ Warm(context.Context) error })
	return warmer, ok
}

// warmDecider completes the engine handshake once so the first decision does
// not pay for weight loading; a failure is logged and the engine stays
// available for a later attempt.
func (b *serveBuilder) warmDecider(ctx context.Context) error {
	warmer, ok := b.deciderWarmer()
	if !ok {
		return nil
	}
	if client, ok := b.decider.(*bialy.Client); ok {
		// Packaged apps bundle the checkpoint; a missing one is provisioned here.
		switch client.Status() {
		case bialy.ReasonDisabled, bialy.ReasonBinaryMissing:
			return nil
		case bialy.ReasonModelMissing:
			cfg := client.Config()
			slog.InfoContext(ctx, "decision checkpoint not installed; provisioning", "model", cfg.ModelID, "dir", cfg.ModelDir)
			if err := bialy.EnsureModel(ctx, cfg.ModelDir, nil); err != nil {
				if ctx.Err() == nil {
					slog.WarnContext(ctx, "decision checkpoint not provisioned; decisions abstain and ranking sites stay lexical", "error", err)
				}
				return nil
			}
		default:
		}
	}
	if err := warmer.Warm(ctx); err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "decision engine did not warm", "error", err)
	}
	return nil
}
