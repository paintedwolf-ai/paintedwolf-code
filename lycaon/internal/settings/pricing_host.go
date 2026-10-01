package settings

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/pricing"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// PricingHost applies cached rates immediately and refreshes feeds in the background.
type PricingHost struct {
	mu sync.Mutex

	Store     *PricingStore
	Catalog   pricing.SourcesConfig
	CacheDir  string
	Live      cost.LiveRateSource
	Kinds     cost.KindResolver
	ModelFeed pricing.ModelFeed
	Tracker   cost.PricerSetter
	GetBytes  func(context.Context, string) ([]byte, error) // nil uses the guarded feed fetcher.

	// RefreshInterval paces StartAutoRefresh; zero means hourly.
	RefreshInterval time.Duration

	reg *pricing.Registry
	// regCtx bounds every fetch started against reg; teardown cancels it.
	regCtx     context.Context
	cancel     context.CancelFunc
	refreshing map[string]int
	onSettled  func()
	refreshWg  sync.WaitGroup
	// stopAuto ends the StartAutoRefresh loop; the loop counts in refreshWg.
	stopAuto context.CancelFunc
	closed   bool
}

// SetOnRefreshSettled installs a callback run after a background fetch settles.
func (h *PricingHost) SetOnRefreshSettled(fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onSettled = fn
}

// StartAutoRefresh periodically syncs expired pricing sources.
func (h *PricingHost) StartAutoRefresh(ctx context.Context) {
	interval := h.RefreshInterval
	if interval <= 0 {
		interval = time.Hour
	}
	h.mu.Lock()
	if h.closed || h.stopAuto != nil {
		h.mu.Unlock()
		return
	}
	ctx, h.stopAuto = context.WithCancel(ctx)
	h.refreshWg.Add(1)
	h.mu.Unlock()
	go func() {
		defer h.refreshWg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = h.SyncFromStore(ctx)
			}
		}
	}()
}

// SyncFromStore installs a pricer from current settings and starts
// background fetches for selected sources without a current cache.
func (h *PricingHost) SyncFromStore(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.syncLocked(ctx)
}

func (h *PricingHost) syncLocked(ctx context.Context) error {
	if h.closed || h.Tracker == nil || h.Store == nil {
		return nil
	}
	eff := h.Store.Effective()
	if !eff.CostTrackingEnabled {
		h.teardownLocked()
		h.Tracker.SetPricer(cost.TrackingDisabledPricer{Kinds: h.Kinds})
		return nil
	}
	if err := h.ensureRegistryLocked(ctx); err != nil {
		return err
	}
	h.Tracker.SetPricer(h.chainFromStoreLocked(eff))
	h.startRefreshesLocked(eff)
	return nil
}

func (h *PricingHost) teardownLocked() {
	if h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}
	h.reg = nil
	h.regCtx = nil
	h.refreshing = nil
}

// Close stops the refresh loop, cancels active fetches, and waits for both;
// a closed host starts no more.
func (h *PricingHost) Close() {
	h.mu.Lock()
	h.closed = true
	if h.stopAuto != nil {
		h.stopAuto()
	}
	if h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}
	h.mu.Unlock()
	h.refreshWg.Wait()
	h.mu.Lock()
	h.teardownLocked()
	h.mu.Unlock()
}

func (h *PricingHost) ensureRegistryLocked(ctx context.Context) error {
	if h.reg != nil {
		return nil
	}
	cfg := h.Catalog
	if len(cfg.Sources) == 0 {
		return fmt.Errorf("pricing catalog is empty")
	}
	cacheDir := h.CacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(".", enginepaths.PricingCacheDirName)
	}
	reg, err := pricing.NewRegistryFromConfig(ctx, cfg, pricing.RegistryOptions{
		CacheDir:  cacheDir,
		GetBytes:  h.GetBytes,
		ModelFeed: h.ModelFeed,
	})
	if err != nil {
		return err
	}
	h.reg = reg
	h.refreshing = map[string]int{}
	// Fetches outlive the request that selected the source; teardown ends them.
	h.regCtx, h.cancel = context.WithCancel(context.WithoutCancel(ctx))
	return nil
}

func (h *PricingHost) startRefreshesLocked(eff PricingEffective) {
	for _, src := range eff.Sources {
		if !src.Enabled || h.refreshing[src.ID] > 0 {
			continue
		}
		if table, ok := h.reg.CachedTable(src.ID); ok && table.Status == pricing.StatusOK {
			continue
		}
		h.refreshing[src.ID]++
		h.refreshWg.Add(1)
		go func(ctx context.Context, reg *pricing.Registry, id string) {
			defer h.refreshWg.Done()
			_, _ = reg.Refresh(ctx, id)
			h.settleFetch(reg, id)
		}(h.regCtx, h.reg, src.ID)
	}
}

// settleFetch reinstalls the pricer after a fetch unless the registry was replaced.
func (h *PricingHost) settleFetch(reg *pricing.Registry, id string) {
	h.mu.Lock()
	if h.reg != reg {
		h.mu.Unlock()
		return
	}
	if h.refreshing[id]--; h.refreshing[id] <= 0 {
		delete(h.refreshing, id)
	}
	h.Tracker.SetPricer(h.chainFromStoreLocked(h.Store.Effective()))
	notify := h.onSettled
	h.mu.Unlock()
	if notify != nil {
		notify()
	}
}

func (h *PricingHost) chainFromStoreLocked(eff PricingEffective) cost.Pricer {
	for _, src := range eff.Sources {
		if !src.Enabled || h.reg == nil {
			continue
		}
		table, ok := h.reg.CachedTable(src.ID)
		if !ok || len(table.Rates) == 0 {
			continue
		}
		selected := &cost.PricedSource{ID: src.ID, Table: table}
		return cost.NewChainPricer(h.Live, h.Kinds, selected)
	}
	return cost.NewChainPricer(h.Live, h.Kinds, nil)
}

// ApplySettings persists the overlay and resyncs the pricer.
func (h *PricingHost) ApplySettings(ctx context.Context, overlay PricingUserOverlay) error {
	if h.Store == nil {
		return fmt.Errorf("pricing store not configured")
	}
	if err := h.Store.PutGlobal(overlay); err != nil {
		return err
	}
	return h.SyncFromStore(ctx)
}

// AvailableSources returns catalog meta including disabled entries.
func (h *PricingHost) AvailableSources() []wire.PricingSourceMeta {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.availableSourcesLocked()
}

func (h *PricingHost) availableSourcesLocked() []wire.PricingSourceMeta {
	if h.Store == nil {
		return nil
	}
	eff := h.Store.Effective()
	enabled := map[string]bool{}
	for _, s := range eff.Sources {
		enabled[s.ID] = s.Enabled
	}
	out := make([]wire.PricingSourceMeta, 0, len(h.Store.Catalog()))
	for _, ent := range h.Store.Catalog() {
		meta := wire.PricingSourceMeta{
			ID:         ent.ID,
			Label:      ent.Label,
			Kind:       ent.Kind,
			Enabled:    enabled[ent.ID],
			Status:     wire.PricingSourceStatusOffline,
			Refreshing: h.refreshing[ent.ID] > 0,
		}
		if h.reg != nil {
			if table, ok := h.reg.CachedTable(ent.ID); ok {
				meta.Status = wire.PricingSourceStatus(table.Status)
				meta.ModelCount = len(table.Rates)
				if !table.FetchedAt.IsZero() {
					t := table.FetchedAt.UTC()
					meta.FetchedAt = &t
				}
				if !table.SourceLastUpdated.IsZero() {
					t := table.SourceLastUpdated.UTC()
					meta.UpdatedAt = &t
				}
			}
		}
		out = append(out, meta)
	}
	return out
}

// Response returns effective settings and source status.
func (h *PricingHost) Response() wire.SettingsPricingResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	eff := h.Store.Effective()
	sources := make([]wire.SettingsPricingSource, 0, len(eff.Sources))
	for _, s := range eff.Sources {
		sources = append(sources, wire.SettingsPricingSource{ID: s.ID, Enabled: s.Enabled})
	}
	return wire.SettingsPricingResponse{
		CostTrackingEnabled: eff.CostTrackingEnabled,
		CostTrackingSinceAt: eff.CostTrackingSince,
		Sources:             sources,
		AvailableSources:    h.availableSourcesLocked(),
	}
}

// RefreshSource fetches one feed outside the host lock when tracking is on.
func (h *PricingHost) RefreshSource(ctx context.Context, id string) (wire.PricingSourceMeta, error) {
	reg, regCtx, err := h.beginManualRefresh(ctx, id)
	if err != nil {
		return wire.PricingSourceMeta{}, err
	}
	fetchCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(regCtx, cancel)
	_, err = reg.ForceRefresh(fetchCtx, id)
	stop()
	cancel()
	h.settleFetch(reg, id)
	h.mu.Lock()
	meta := h.metaForLocked(id)
	h.mu.Unlock()
	return meta, err
}

func (h *PricingHost) beginManualRefresh(ctx context.Context, id string) (*pricing.Registry, context.Context, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.Store == nil {
		return nil, nil, fmt.Errorf("pricing store not configured")
	}
	known := false
	for _, e := range h.Store.Catalog() {
		if e.ID == id {
			known = true
			break
		}
	}
	if !known {
		return nil, nil, fmt.Errorf("%w: %q", pricing.ErrUnknownID, id)
	}
	eff := h.Store.Effective()
	if !eff.CostTrackingEnabled {
		return nil, nil, ErrCostTrackingDisabled
	}
	enabled := false
	for _, source := range eff.Sources {
		if source.ID == id {
			enabled = source.Enabled
			break
		}
	}
	if !enabled {
		return nil, nil, ErrPricingSourceDisabled
	}
	if err := h.ensureRegistryLocked(ctx); err != nil {
		return nil, nil, err
	}
	h.refreshing[id]++
	return h.reg, h.regCtx, nil
}

func (h *PricingHost) metaForLocked(id string) wire.PricingSourceMeta {
	for _, m := range h.availableSourcesLocked() {
		if m.ID == id {
			return m
		}
	}
	return wire.PricingSourceMeta{ID: id, Status: wire.PricingSourceStatusOffline}
}

// RefreshErrorCode maps a pricing refresh failure to its API error code;
// internal_error means the caller logs err as a host fault.
func RefreshErrorCode(err error) wire.ApiErrorCode {
	switch {
	case errors.Is(err, pricing.ErrInvalid):
		return wire.ApiErrorCodePricingSourceInvalid
	case errors.Is(err, pricing.ErrUnreachable):
		return wire.ApiErrorCodePricingSourceUnreachable
	case errors.Is(err, pricing.ErrUnknownID):
		return wire.ApiErrorCodePricingSourceNotFound
	case errors.Is(err, ErrPricingSourceDisabled), errors.Is(err, ErrCostTrackingDisabled):
		return wire.ApiErrorCodePricingSourceDisabled
	default:
		return wire.ApiErrorCodeInternalError
	}
}
