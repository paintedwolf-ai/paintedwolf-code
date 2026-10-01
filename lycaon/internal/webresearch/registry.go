package webresearch

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/catalogruntime"
)

// SearchProvider is one custom catalog backend.
type SearchProvider interface {
	ID() string
	Kind() ProviderKind
	Configured(s Settings) bool
	Search(ctx context.Context, s Settings, query string, maxResults int) providerOutcome
}

// Registry holds catalog-backed custom providers.
type Registry struct {
	catalog   *Catalog
	providers *catalogruntime.Registry[SearchProvider]
	gates     map[string]*providerGate
	health    map[string]*providerHealth
	quota     ProviderQuotaStore
}

// NewRegistry builds an empty registry for a catalog.
func NewRegistry(catalog *Catalog) *Registry {
	return &Registry{
		catalog:   catalog,
		providers: catalogruntime.NewRegistry[SearchProvider](),
		gates:     make(map[string]*providerGate),
		health:    make(map[string]*providerHealth),
	}
}

// AttachQuotaStore wires persisted daily counters for provider pacing gates.
func (r *Registry) AttachQuotaStore(store ProviderQuotaStore) {
	if r == nil {
		return
	}
	r.quota = store
	for id, gate := range r.gates {
		gate.quota = store
		r.gates[id] = gate
	}
}

// Register adds or replaces a provider adapter by stable id.
func (r *Registry) Register(p SearchProvider) {
	if r == nil || p == nil {
		return
	}
	_ = r.providers.Register(p.ID(), p)
}

func isDirectProvider(id string) bool { return id == directWireProviderID }

// Get returns a provider by wire id.
func (r *Registry) Get(id string) SearchProvider {
	if r == nil {
		return nil
	}
	p, _ := r.providers.Get(id)
	return p
}

// Has reports whether id is registered.
func (r *Registry) Has(id string) bool {
	return r.Get(id) != nil
}

// IDs returns registered provider ids sorted.
func (r *Registry) IDs() []string {
	if r == nil {
		return nil
	}
	return r.providers.IDs()
}

// Catalog returns the bundled catalog.
func (r *Registry) Catalog() *Catalog {
	if r == nil {
		return nil
	}
	return r.catalog
}

// ResetProviderRuntime clears transient health and pacing cooldowns.
func (r *Registry) ResetProviderRuntime(providerID string) {
	if r == nil {
		return
	}
	if health := r.health[providerID]; health != nil {
		health.reset()
	}
	gateID := providerID
	if r.catalog != nil {
		entry, ok := r.catalog.Entry(providerID)
		if ok && entry.Pacing != nil {
			if sharedKey := strings.TrimSpace(entry.Pacing.SharedKey); sharedKey != "" {
				gateID = sharedKey
			}
		}
	}
	if gate := r.gates[gateID]; gate != nil {
		gate.resetCooldown()
	}
}

// customBuilders registers providers of the custom family, keyed by provider id.
var customBuilders = map[string]func(*Registry){
	"brave":           func(r *Registry) { r.registerREST(braveRESTSpec(), KindKeyed) },
	"marginalia":      func(r *Registry) { r.registerREST(marginaliaRESTSpec(), KindKeyed) },
	"google_cse":      func(r *Registry) { r.registerREST(googleCseRESTSpec(), KindKeyedExtra) },
	"serper":          func(r *Registry) { r.registerREST(serperRESTSpec(), KindKeyed) },
	"searxng":         func(r *Registry) { r.registerREST(searxngRESTSpec(), KindKeylessEndpoint) },
	"mwmbl":           func(r *Registry) { r.registerREST(mwmblRESTSpec(), KindKeyless) },
	"hn":              func(r *Registry) { r.registerREST(hnRESTSpec(), KindKeyless) },
	"mankier":         func(r *Registry) { r.registerREST(mankierRESTSpec(), KindKeyless) },
	"ietf_rfc":        func(r *Registry) { r.registerProvider(newIETFRFCProvider()) },
	"microsoft_learn": func(r *Registry) { r.registerREST(microsoftLearnRESTSpec(), KindKeyless) },
	"arxiv":           func(r *Registry) { r.registerREST(arxivRESTSpec(), KindKeyless) },
	"mdn":             func(r *Registry) { r.registerREST(mdnRESTSpec(), KindKeyless) },
	"tavily":          func(r *Registry) { r.registerREST(tavilyRESTSpec(), KindKeyed) },
	"kagi":            func(r *Registry) { r.registerREST(kagiRESTSpec(), KindKeyed) },
	"github":          func(r *Registry) { r.registerREST(githubRESTSpec(), KindKeyless) },
	"gitlab":          func(r *Registry) { r.registerREST(gitlabRESTSpec(), KindKeyless) },
	"gdelt":           func(r *Registry) { r.registerREST(gdeltRESTSpec(), KindKeyless) },
	"openalex":        func(r *Registry) { r.registerREST(openalexRESTSpec(), KindKeyless) },
	"crossref":        func(r *Registry) { r.registerREST(crossrefRESTSpec(), KindKeyless) },
	"europepmc":       func(r *Registry) { r.registerREST(europepmcRESTSpec(), KindKeyless) },
	"wikidata":        func(r *Registry) { r.registerREST(wikidataRESTSpec(), KindKeyless) },
	"internetarchive": func(r *Registry) { r.registerREST(internetArchiveRESTSpec(), KindKeyless) },
	"openlibrary":     func(r *Registry) { r.registerREST(openLibraryRESTSpec(), KindKeyless) },
	"huggingface":     func(r *Registry) { r.registerREST(huggingfaceRESTSpec(), KindKeyless) },
	"crates_io":       func(r *Registry) { r.registerREST(cratesIoRESTSpec(), KindKeyless) },
	"maven_central":   func(r *Registry) { r.registerREST(mavenCentralRESTSpec(), KindKeyless) },
	"cran":            func(r *Registry) { r.registerREST(cranRESTSpec(), KindKeyless) },
	"metacpan":        func(r *Registry) { r.registerREST(metacpanRESTSpec(), KindKeyless) },
	"nvd":             func(r *Registry) { r.registerREST(nvdRESTSpec(), KindKeyless) },
	"css_tricks":      func(r *Registry) { r.registerREST(cssTricksRESTSpec(), KindKeyless) },
	"caniuse":         func(r *Registry) { r.registerREST(caniuseRESTSpec(), KindKeyless) },
}

// RegisterCatalogProviders registers every catalog provider.
func RegisterCatalogProviders(r *Registry) error {
	if r == nil || r.catalog == nil {
		return nil
	}
	for _, entry := range r.catalog.Entries() {
		if err := r.registerCatalogEntry(entry); err != nil {
			return fmt.Errorf("register catalog provider %q: %w", entry.ID, err)
		}
	}
	return nil
}

type catalogProviderBuild struct {
	registry *Registry
	entry    CatalogEntry
}

type catalogProviderBuilt struct{}

var catalogProviderFactories = catalogruntime.NewFactorySet(
	map[string]catalogruntime.Factory[catalogProviderBuild, catalogProviderBuilt]{
		string(FamilyDiscourse): func(_ context.Context, build catalogProviderBuild) (catalogProviderBuilt, error) {
			build.registry.registerREST(discourseRESTSpec(build.entry.ID), build.entry.Kind)
			return catalogProviderBuilt{}, nil
		},
		string(FamilyStackExchange): func(_ context.Context, build catalogProviderBuild) (catalogProviderBuilt, error) {
			site := strings.TrimSpace(build.entry.FamilyParams["site"])
			build.registry.registerREST(stackexchangeRESTSpec(build.entry.ID, site), build.entry.Kind)
			return catalogProviderBuilt{}, nil
		},
		string(FamilyMediawikiREST): func(_ context.Context, build catalogProviderBuild) (catalogProviderBuilt, error) {
			build.registry.registerREST(mediawikiRESTSpec(build.entry.ID), build.entry.Kind)
			return catalogProviderBuilt{}, nil
		},
		string(FamilyMediawikiAction): func(_ context.Context, build catalogProviderBuild) (catalogProviderBuilt, error) {
			build.registry.registerREST(mediawikiActionSpec(build.entry.ID), build.entry.Kind)
			return catalogProviderBuilt{}, nil
		},
		string(FamilyPackageRegistry): func(_ context.Context, build catalogProviderBuild) (catalogProviderBuilt, error) {
			spec, err := packageRegistryRESTSpec(build.entry)
			if err != nil {
				return catalogProviderBuilt{}, err
			}
			build.registry.registerREST(spec, build.entry.Kind)
			return catalogProviderBuilt{}, nil
		},
		string(FamilyCustom): func(_ context.Context, build catalogProviderBuild) (catalogProviderBuilt, error) {
			builder, ok := customBuilders[build.entry.ID]
			if !ok {
				return catalogProviderBuilt{}, fmt.Errorf("family custom has no builder")
			}
			builder(build.registry)
			return catalogProviderBuilt{}, nil
		},
	},
	nil,
)

func (r *Registry) registerCatalogEntry(entry CatalogEntry) error {
	_, err := catalogProviderFactories.Build(
		context.Background(), string(entry.EffectiveFamily()), catalogProviderBuild{registry: r, entry: entry},
	)
	return err
}

// ensureHealthGate returns the shared health state for a provider.
func (r *Registry) ensureHealthGate(providerID string) *providerHealth {
	gate := r.health[providerID]
	if gate == nil {
		gate = newProviderHealth(providerID, nil)
		r.health[providerID] = gate
	}
	return gate
}

func (r *Registry) registerREST(spec RESTSpec, kind ProviderKind) {
	r.registerProvider(NewRESTSearchProvider(spec, kind))
}

func (r *Registry) registerProvider(p SearchProvider) {
	if r == nil || p == nil {
		return
	}
	if rest, ok := p.(*RESTSearchProvider); ok {
		if entry, hasEntry := r.catalog.Entry(rest.ID()); hasEntry {
			rest.allowPrivateEndpoint = entry.AllowPrivateEndpoint
		}
	}
	entry, hasEntry := r.catalog.Entry(p.ID())
	if hasEntry && entry.Pacing != nil {
		gateID := entry.ID
		if sharedKey := strings.TrimSpace(entry.Pacing.SharedKey); sharedKey != "" {
			gateID = sharedKey
		}
		gate := r.gates[gateID]
		if gate == nil {
			gate = newProviderGate(gateID, entry.Pacing, r.quota, nil)
			r.gates[gateID] = gate
		}
		p = &gatedSearchProvider{inner: p, gate: gate, entry: entry}
	}
	// Open health gates do not consume pacing quota.
	p = &healthWrappedProvider{inner: p, health: r.ensureHealthGate(p.ID())}
	minWords := defaultQueryBackoffMinWords
	if hasEntry && entry.QueryBackoff != nil {
		minWords = entry.QueryBackoff.MinWords
	}
	p = &queryBackoffProvider{inner: p, minWords: minWords}
	r.Register(p)
}
