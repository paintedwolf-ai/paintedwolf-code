package llm

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/catalogruntime"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/destconfig"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/pkg/api"
)

// Registry implements ProviderRegistry with catalog + credentials.
type Registry struct {
	thinkingPolicy  atomic.Pointer[PolicyStore]
	rateGate        *providerretry.ProviderRateGate
	mutationMu      sync.Mutex
	screenMu        sync.RWMutex
	catalog         *ProviderCatalog
	credentials     *providercredentials.Store
	snapshot        atomic.Pointer[providerRegistrySnapshot]
	discovery       *discovery.Cache
	cloudflareUsage *openaicompat.CloudflareUsageCache
	discoveryClient *http.Client
	roleExclusions  RoleExclusions
	// modelFeed has independent synchronization.
	modelFeed atomic.Pointer[modelfeed.Feed]
	listCache *catalogruntime.SnapshotCache[[]api.ProviderMeta]
	// outboundSecretScreen is the final provider boundary.
	outboundSecretScreen OutboundSecretScreen
}

const providerListCacheTTL = 30 * time.Second

// Usage-probe budget during provider listing.
const cloudflareUsageFetchTimeout = 2 * time.Second

// NewRegistry builds providers from catalog and stored credentials.
func NewRegistry(catalog *ProviderCatalog, credentials *providercredentials.Store) (*Registry, error) {
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
	// Build the initial snapshot without a request context.
	if err := r.rebuild(context.Background()); err != nil {
		return nil, err
	}
	return r, nil
}

// RoleExclusions returns the registry's model restrictions.
func (r *Registry) RoleExclusions() RoleExclusions {
	if r == nil {
		return RoleExclusions{}
	}
	return cloneRoleExclusions(r.roleExclusions)
}

// SetModelFeed sets the shared model feed.
func (r *Registry) SetModelFeed(feed *modelfeed.Feed) {
	r.modelFeed.Store(feed)
	r.InvalidateListCache()
	if feed == nil {
		return
	}
	feed.AddRefreshListener(func() {
		if r.modelFeed.Load() != feed {
			return
		}
		_ = r.rebuild(context.Background())
	})
	// Publish an existing disk snapshot immediately.
	_ = r.rebuild(context.Background())
}

// SetOutboundSecretScreen sets the final plaintext screen.
func (r *Registry) SetOutboundSecretScreen(screen OutboundSecretScreen) {
	if r == nil {
		return
	}
	r.screenMu.Lock()
	r.outboundSecretScreen = screen
	r.screenMu.Unlock()
}

// ProviderKind returns the kind for a catalog instance.
func (r *Registry) ProviderKind(instanceID string) (string, bool) {
	if r == nil {
		return "", false
	}
	if r.catalog != nil {
		if entry, ok := r.catalog.Get(instanceID); ok {
			kind := strings.TrimSpace(entry.Kind)
			if kind != "" {
				return kind, true
			}
		}
	}
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return "", false
	}
	entry, ok := snapshot.entries[instanceID]
	if !ok {
		return "", false
	}
	kind := strings.TrimSpace(entry.Kind)
	if kind == "" {
		return "", false
	}
	return kind, true
}

// ModelFeed returns the shared model feed.
func (r *Registry) ModelFeed() *modelfeed.Feed {
	if r == nil {
		return nil
	}
	return r.modelFeed.Load()
}

// resolveAPIKey returns the Settings-stored API key for the provider.
// Catalog environment hints do not configure providers.
func (r *Registry) resolveAPIKey(entry CatalogEntry) string {
	if r.credentials == nil {
		return ""
	}
	if v, ok := r.credentials.Get(entry.ID); ok {
		return strings.TrimSpace(v.Value())
	}
	return ""
}

// ConfiguredHosts returns hostnames for configured provider endpoints.
func (r *Registry) ConfiguredHosts() []string {
	if r == nil {
		return nil
	}
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return nil
	}
	var out []string
	for _, id := range snapshot.ids {
		entry, exists := snapshot.entries[id]
		if !exists || !snapshot.configured[id] {
			continue
		}
		if entry.EndpointStyle.Normalize() != EndpointStyleURL {
			continue
		}
		if h := destconfig.Normalize(entry.BaseURL); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// AnyConfigured reports whether any provider is configured.
func (r *Registry) AnyConfigured() bool {
	return r.ConfiguredCount() > 0
}

// ConfiguredCount returns the number of callable providers.
func (r *Registry) ConfiguredCount() int {
	if r == nil {
		return 0
	}
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return 0
	}
	count := 0
	for _, configured := range snapshot.configured {
		if configured {
			count++
		}
	}
	return count
}

// IsConfigured reports whether a provider can make API calls.
func (r *Registry) IsConfigured(id string) bool {
	if r == nil {
		return false
	}
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return false
	}
	return snapshot.configured[strings.TrimSpace(id)]
}

// Get returns a typed configuration error for an unavailable provider.
func (r *Registry) Get(id string) (modelcall.Provider, error) {
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return nil, &failure.ProviderNotConfiguredError{ProviderID: id}
	}
	p, ok := snapshot.providers[id]
	if !ok {
		return nil, &failure.ProviderNotConfiguredError{ProviderID: id}
	}
	return r.decorateProvider(snapshot, p), nil
}

// Default returns the default configured provider.
func (r *Registry) Default() modelcall.Provider {
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return nil
	}
	if p, ok := snapshot.providers[snapshot.defaultID]; ok {
		return r.decorateProvider(snapshot, p)
	}
	for _, id := range snapshot.ids {
		return r.decorateProvider(snapshot, snapshot.providers[id])
	}
	return nil
}

// decorateProvider screens requests before starting the budget that encloses response retries.
func (r *Registry) decorateProvider(snapshot *providerRegistrySnapshot, p modelcall.Provider) modelcall.Provider {
	if p == nil {
		return nil
	}
	var entry CatalogEntry
	var hasEntry bool
	if snapshot != nil {
		entry, hasEntry = snapshot.entries[p.ID()]
	}
	if hasEntry {
		if len(entry.RejectionReasons) > 0 {
			p = &rejectionReasonProvider{inner: p, rules: entry.RejectionReasons}
		}
		p = &responseRetryProvider{inner: p, policy: entry.HTTPRetry.Clone()}
	}
	p = &dispatchProvider{Provider: &budgetedProvider{inner: p}}
	r.screenMu.RLock()
	screen := r.outboundSecretScreen
	r.screenMu.RUnlock()
	if screen == nil {
		return &lifecycleProvider{Provider: &reasoningMarkerProvider{Provider: &thinkingPolicyProvider{Provider: p, registry: r}}}
	}
	destination := ScreenDestination{ID: p.ID()}
	if hasEntry {
		destination = ScreenDestination{
			ID:         entry.SecretDestinationID(),
			Label:      strings.TrimSpace(entry.Label),
			ProviderID: strings.TrimSpace(entry.ID),
			Trusted:    entry.SecretScreenTrusted(),
		}
	}
	p = &secretScreenedProvider{inner: p, destination: destination, screen: screen}
	return &lifecycleProvider{Provider: &reasoningMarkerProvider{Provider: &thinkingPolicyProvider{Provider: p, registry: r}}}
}

// Catalog exposes the underlying catalog.
func (r *Registry) Catalog() *ProviderCatalog {
	return r.catalog
}

// StreamWithSelection streams from the configured provider.
func (r *Registry) StreamWithSelection(ctx context.Context, sel *ModelSelection, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	if sel == nil {
		return nil, fmt.Errorf("nil model selection")
	}
	providerID := strings.TrimSpace(sel.ProviderID)
	configured, err := r.ensureConfigured(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if !configured {
		return nil, &failure.ProviderNotConfiguredError{ProviderID: providerID}
	}
	p, err := r.Get(providerID)
	if err != nil {
		return nil, err
	}
	req.Model = sel.Model
	req.Messages = transcript.Project(req.Messages)
	return p.Stream(ctx, req)
}

var _ modelcall.ProviderRegistry = (*Registry)(nil)
