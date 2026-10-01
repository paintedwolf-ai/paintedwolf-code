package pricing

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/catalogruntime"
)

// RegistryOptions configures NewRegistryFromConfig.
type RegistryOptions struct {
	CacheDir string
	// GetBytes overrides the feed fetch boundary for tests.
	GetBytes func(context.Context, string) ([]byte, error)
	Now      func() time.Time
	TTL      time.Duration
	Timeout  time.Duration
	MaxBytes int64
	// ModelFeed supplies the shared metadata projection.
	ModelFeed ModelFeed
}

// Registry holds feed sources and their last-good RateTables.
type Registry struct {
	mu       sync.RWMutex
	sources  *catalogruntime.Registry[Source]
	order    []string
	tables   map[string]RateTable
	cacheDir string
	ttl      time.Duration
	now      func() time.Time
}

// NewRegistryFromConfig builds a registry from a validated catalog.
func NewRegistryFromConfig(ctx context.Context, cfg SourcesConfig, opts RegistryOptions) (*Registry, error) {
	catalog, err := assembleSourceCatalog(cfg.Sources)
	if err != nil {
		return nil, err
	}
	cfg.Sources = sourceSpecs(catalog)
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.TTL <= 0 {
		opts.TTL = PricingCacheTTL
	}
	if opts.Timeout <= 0 {
		opts.Timeout = PricingFetchTimeout
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = MaxPricingPayloadBytes
	}

	fetch := pricingFetch(opts)

	r := &Registry{
		sources:  catalogruntime.NewRegistry[Source](),
		tables:   make(map[string]RateTable, len(cfg.Sources)),
		cacheDir: opts.CacheDir,
		ttl:      opts.TTL,
		now:      opts.Now,
	}
	for _, sc := range cfg.Sources {
		src, err := sourceFactories.Build(ctx, sc.Kind, sourceBuild{
			config: sc, options: opts, fetch: fetch,
		})
		if err != nil {
			return nil, err
		}
		if err := r.Register(src); err != nil {
			return nil, err
		}
		// Corrupt caches are rebuilt on the next source refresh.
		if table, ok, err := readDiskCache(opts.CacheDir, sc.ID); err == nil && ok {
			r.tables[sc.ID] = table
		}
	}
	return r, nil
}

// Register adds a source. Duplicate ids are rejected.
func (r *Registry) Register(src Source) error {
	if src == nil {
		return fmt.Errorf("pricing: nil source")
	}
	id := src.ID()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.sources.Add(id, src); err != nil {
		return fmt.Errorf("pricing: duplicate source id %q", id)
	}
	r.order = append(r.order, id)
	return nil
}

// Get returns a registered source.
func (r *Registry) Get(id string) (Source, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sources.Get(id)
}

// List returns sources in catalog order.
func (r *Registry) List() []Source {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Source, 0, len(r.order))
	for _, id := range r.order {
		if source, ok := r.sources.Get(id); ok {
			out = append(out, source)
		}
	}
	return out
}

// Refresh fetches one source and updates the disk cache on success.
func (r *Registry) Refresh(ctx context.Context, id string) (RateTable, error) {
	return r.refresh(ctx, id, false)
}

// ForceRefresh re-fetches even when the shared modelfeed document is still fresh.
func (r *Registry) ForceRefresh(ctx context.Context, id string) (RateTable, error) {
	return r.refresh(ctx, id, true)
}

func (r *Registry) refresh(ctx context.Context, id string, force bool) (RateTable, error) {
	r.mu.RLock()
	src, ok := r.sources.Get(id)
	prior, hasPrior := r.tables[id]
	r.mu.RUnlock()
	if !ok {
		return RateTable{}, fmt.Errorf("%w: %q", ErrUnknownID, id)
	}

	var table RateTable
	var err error
	if force {
		if mds, ok := src.(*modelsDevSource); ok {
			table, err = mds.ForceRefresh(ctx)
		} else {
			table, err = src.Fetch(ctx)
		}
	} else {
		table, err = src.Fetch(ctx)
	}
	if err != nil {
		status := StatusOffline
		if hasPrior {
			status = StatusError
		}
		if errors.Is(err, ErrInvalid) {
			status = StatusError
		}
		r.mu.Lock()
		if hasPrior {
			prior.Status = status
			r.tables[id] = prior
		} else {
			r.tables[id] = RateTable{Rates: map[RateKey]Rate{}, Status: status}
		}
		out := r.tables[id]
		r.mu.Unlock()
		return out, err
	}

	table.Status = StatusOK
	if err := writeDiskCache(r.cacheDir, id, table); err != nil {
		return RateTable{}, err
	}
	r.mu.Lock()
	r.tables[id] = table
	r.mu.Unlock()
	return table, nil
}

// CachedTable returns the last-good table, marking stale when past TTL.
func (r *Registry) CachedTable(id string) (RateTable, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	table, ok := r.tables[id]
	if !ok {
		return RateTable{}, false
	}
	out := table
	if out.Status == StatusOK && !out.FetchedAt.IsZero() && r.now().Sub(out.FetchedAt) > r.ttl {
		out.Status = StatusStale
	}
	return out, true
}
