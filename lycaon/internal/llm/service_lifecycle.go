package llm

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/catalogruntime"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/modelfeed"
)

// NewService loads catalog, credentials, policy, registry, and router.
func NewService(ctx context.Context, mock modelcall.LLMClient) (*Service, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	catalog, err := NewProviderCatalog()
	if err != nil {
		return nil, err
	}
	credentials, err := providercredentials.New()
	if err != nil {
		return nil, err
	}
	policy, err := NewPolicyStore()
	if err != nil {
		return nil, err
	}
	registry, err := NewRegistry(ctx, catalog, credentials)
	if err != nil {
		return nil, err
	}
	registry.thinkingPolicy.Store(policy)
	feed, feedErr := modelfeed.New(modelfeed.Options{})
	if feedErr != nil {
		return nil, fmt.Errorf("modelfeed: %w", feedErr)
	}
	registry.SetModelFeed(ctx, feed)
	var refreshCancel context.CancelFunc
	var refreshDone <-chan struct{}
	if ProviderUtilityCallsEnabled() {
		refreshCancel, refreshDone = startModelFeedRefresh(ctx, feed)
	}
	router := NewStaticModelRouter(policy)

	svc := &Service{
		modelFeedRefreshCancel: refreshCancel,
		modelFeedRefreshDone:   refreshDone,
		Catalog:                catalog,
		Credentials:            credentials,
		Registry:               registry,
		Policy:                 policy,
		Router:                 router,
		Mock:                   WrapLLMClientIfDebug(mock, "mock"),
		Utility:                NewUtilityPlane(),
		Capacity:               NewCapacityGate(catalog.CapacityPolicy()),
		Refusals:               providerretry.NewModelRefusalGate(),
	}
	return svc, nil
}

func startModelFeedRefresh(parent context.Context, feed *modelfeed.Feed) (context.CancelFunc, <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = feed.Refresh(ctx)
	}()
	return cancel, done
}

// Close stops the model catalog refresh.
func (s *Service) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.modelFeedRefreshCancel != nil {
		s.modelFeedRefreshCancel()
	}
	if s.Registry != nil && s.Registry.discovery != nil {
		if err := s.Registry.discovery.Close(ctx); err != nil {
			return err
		}
	}
	if s.modelFeedRefreshDone == nil {
		return nil
	}
	select {
	case <-s.modelFeedRefreshDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// NewRegistry builds providers from catalog and stored credentials.
func NewRegistry(ctx context.Context, catalog *ProviderCatalog, credentials *providercredentials.Store) (*Registry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	roleExclusions, err := LoadRoleExclusions()
	if err != nil {
		return nil, err
	}
	rateDirectory := os.Getenv("LYCAON_LLM_RATE_STATE_DIR")
	if rateDirectory != "" && (!configdir.IsHarnessChannel() || !filepath.IsAbs(rateDirectory)) {
		return nil, fmt.Errorf("shared provider rate state requires an absolute directory in the isolated harness")
	}
	r := &Registry{
		rateGate:        providerretry.NewProviderRateGate(rateDirectory),
		catalog:         catalog,
		credentials:     credentials,
		discovery:       discovery.NewCache(nil),
		cloudflareUsage: openaicompat.NewCloudflareUsageCache(),
		discoveryClient: providerhttp.DiscoveryClient(discoverModelsTimeout),
		roleExclusions:  roleExclusions,
		listCache:       catalogruntime.NewSnapshotCache(providerListCacheTTL, cloneProviderMetaList),
	}
	// Initial discovery belongs to the allocating startup.
	if err := r.rebuild(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

// SetModelFeed sets the shared model feed.
func (r *Registry) SetModelFeed(ctx context.Context, feed *modelfeed.Feed) {
	r.modelFeed.Store(feed)
	r.InvalidateListCache()
	if feed == nil {
		return
	}
	feed.AddRefreshListener(func() {
		if r.modelFeed.Load() != feed {
			return
		}
		_ = r.rebuild(context.WithoutCancel(ctx))
	})
	// Publish an existing disk snapshot immediately.
	_ = r.rebuild(ctx)
}
