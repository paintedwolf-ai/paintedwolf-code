package providers

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/db"
	"log/slog"

	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/pricing"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/startupprotocol"
)

type Runtime struct {
	Client  modelcall.LLMClient
	Manual  *llm.ManualProvider
	Service *llm.Service
	Curator llm.Curator
	Costs   cost.CostTracker
	Pricing *settings.PricingHost
}

func (b *Runtime) Build(ctx context.Context, options Options, database *db.Store, dataDir string, settingsSvc *settings.Service, resources ResourceLifetime, recovery *bootrecovery.Registry) error {
	var err error
	mockEnabled := llm.MockEnabled(options.Client)

	if llm.ManualOnlyFromEnv() {
		// Manual harness completions arrive through /harness/llm.
		b.Manual = llm.NewManualProvider()
		b.Client = b.Manual
	} else if mockEnabled {
		mockCfg, err := llm.LoadMockConfig()
		if err != nil {
			return fmt.Errorf("mock llm config: %w", err)
		}
		b.Client = llm.NewMockProvider(mockCfg)
		if options.Client != nil {
			b.Client = options.Client
		}
		b.Client = llm.WrapLLMClientIfDebug(b.Client, "mock")
	}

	b.Service, err = llm.NewService(ctx, b.Client)
	if err != nil {
		return fmt.Errorf("llm service: %w", err)
	}
	service := b.Service
	resources.Track("llm-service", 45, service.Close)
	if !mockEnabled && (b.Service == nil || b.Service.Registry == nil || !b.Service.Registry.AnyConfigured()) {
		slog.WarnContext(ctx, "no LLM provider configured; prompts will fail until a provider API key is set")
	}
	if options.Startup != nil {
		if err := options.Startup.Phase(startupprotocol.PhasePricing); err != nil {
			return fmt.Errorf("startup protocol: %w", err)
		}
	}

	tracker := cost.NewSQLTracker(database, cost.NoopPricer{})
	if options.Pricer != nil {
		tracker = cost.NewSQLTracker(database, options.Pricer)
	}
	if err := recovery.Register(bootrecovery.Entry{
		Name: "llm-call-receipts", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
		Run: tracker.RecoverStartedCalls,
	}); err != nil {
		return err
	}
	b.Costs = tracker

	if settingsSvc == nil || settingsSvc.Pricing == nil {
		return fmt.Errorf("pricing settings store not configured")
	}
	catalog, err := pricing.LoadSourcesConfig()
	if err != nil {
		return fmt.Errorf("pricing catalog: %w", err)
	}
	var live cost.LiveRateSource
	var kinds cost.KindResolver
	var modelFeed pricing.ModelFeed
	if b.Service != nil && b.Service.Registry != nil {
		live = b.Service.Registry
		kinds = b.Service.Registry
		modelFeed = b.Service.Registry.ModelFeed()
	}
	host := &settings.PricingHost{
		Store:     settingsSvc.Pricing,
		Catalog:   catalog,
		CacheDir:  enginepaths.PricingCacheRootUnder(dataDir),
		Live:      live,
		Kinds:     kinds,
		ModelFeed: modelFeed,
		Tracker:   tracker,
	}
	resources.Track("pricing", 44, func(context.Context) error { host.Close(); return nil })
	if options.Pricer == nil {
		if err := host.SyncFromStore(ctx); err != nil {
			return fmt.Errorf("pricing sync: %w", err)
		}
		host.StartAutoRefresh(ctx)
	}
	b.Pricing = host
	return nil
}

func (b *Runtime) RegistryPolicy() (*llm.Registry, *llm.PolicyStore) {
	if b.Service == nil {
		return nil, nil
	}
	return b.Service.Registry, b.Service.Policy
}

func (b *Runtime) BindCurator() {
	if b.Service != nil && b.Service.Registry != nil && b.Service.Policy != nil && llm.ProviderUtilityCallsEnabled() {
		curator := b.Service.BindSummarizer(&llm.RegistrySummarizer{
			Fallback: compaction.TruncateSummarizer{},
			Cost:     b.Costs,
			Purpose:  "curate",
		})
		b.Curator = curator
	}
}

type Options struct {
	Client  modelcall.LLMClient
	Pricer  cost.Pricer
	Startup startupprotocol.Sink
}

type ResourceLifetime interface {
	Track(string, int, func(context.Context) error)
}
