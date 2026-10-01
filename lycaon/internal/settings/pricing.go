package settings

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/pricing"
	"gopkg.in/yaml.v3"
)

// ErrPricingNoSource is returned when enabling tracking with zero enabled sources.
var ErrPricingNoSource = errors.New("enable at least one pricing source before turning cost tracking on")

// ErrPricingMultipleSources is returned when more than one pricing source is selected.
var ErrPricingMultipleSources = errors.New("pricing permits at most one selected source")

// ErrPricingSourceDisabled is returned when explicitly refreshing a disabled feed.
var ErrPricingSourceDisabled = errors.New("pricing source is disabled")

// ErrPricingUnknownSource reports an unknown pricing source.
var ErrPricingUnknownSource = errors.New("unknown pricing source")

// ErrCostTrackingDisabled is returned when refresh requires active tracking.
var ErrCostTrackingDisabled = errors.New("cost tracking is disabled")

// PricingSourcePref is one catalog source selection row.
type PricingSourcePref struct {
	ID      string `yaml:"id" json:"id"`
	Enabled bool   `yaml:"enabled" json:"enabled"`
}

// PricingUserOverlay is persisted at ~/.config/paintedwolf/pricing.yaml.
type PricingUserOverlay struct {
	CostTrackingEnabled *bool               `yaml:"cost_tracking_enabled,omitempty"`
	CostTrackingSince   *time.Time          `yaml:"cost_tracking_since,omitempty"`
	Sources             []PricingSourcePref `yaml:"sources,omitempty"`
}

// PricingEffective is the merged settings view for API and runtime.
type PricingEffective struct {
	CostTrackingEnabled bool
	CostTrackingSince   *time.Time
	Sources             []PricingSourcePref
}

// PricingStore loads the feed catalog and optional global user overlay.
type PricingStore struct {
	mu         sync.RWMutex
	catalog    []pricing.SourceConfig
	globalPath string
	global     *PricingUserOverlay
	now        func() time.Time
}

// NewPricingStore loads the catalog and device overlay.
func NewPricingStore() (*PricingStore, error) {
	globalPath, err := userPricingPath()
	if err != nil {
		return nil, err
	}
	return NewPricingStoreAt(globalPath)
}

// NewPricingStoreAt loads the overlay at globalPath.
func NewPricingStoreAt(globalPath string) (*PricingStore, error) {
	cfg, err := pricing.LoadSourcesConfig()
	if err != nil {
		return nil, fmt.Errorf("pricing catalog: %w", err)
	}
	catalog := append([]pricing.SourceConfig(nil), cfg.Sources...)
	store := &PricingStore{
		catalog:    catalog,
		globalPath: globalPath,
		now:        func() time.Time { return time.Now().UTC() },
	}
	if data, err := os.ReadFile(globalPath); err == nil {
		var overlay PricingUserOverlay
		if err := yaml.Unmarshal(data, &overlay); err != nil {
			return nil, fmt.Errorf("parse global pricing: %w", err)
		}
		if err := store.validateOverlayLocked(overlay); err != nil {
			return nil, fmt.Errorf("validate global pricing: %w", err)
		}
		store.global = &overlay
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return store, nil
}

// SetClock selects the timestamp source.
func (s *PricingStore) SetClock(now func() time.Time) {
	if now == nil {
		return
	}
	s.mu.Lock()
	s.now = now
	s.mu.Unlock()
}

// Catalog returns bundled feed entries in catalog order.
func (s *PricingStore) Catalog() []pricing.SourceConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]pricing.SourceConfig, len(s.catalog))
	copy(out, s.catalog)
	return out
}

// Effective returns merged pricing settings.
// With no device overlay, tracking is on and the first catalog source is selected.
func (s *PricingStore) Effective() PricingEffective {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.global == nil {
		return s.defaultEffectiveLocked()
	}

	enabled := false
	var since *time.Time
	overlay := s.global
	if overlay.CostTrackingEnabled != nil {
		enabled = *overlay.CostTrackingEnabled
	}
	if overlay.CostTrackingSince != nil && enabled {
		t := overlay.CostTrackingSince.UTC()
		since = &t
	}

	prefByID := map[string]PricingSourcePref{}
	for _, p := range overlay.Sources {
		if p.ID == "" {
			continue
		}
		prefByID[p.ID] = p
	}

	sources := make([]PricingSourcePref, 0, len(s.catalog))
	for _, ent := range s.catalog {
		pref, ok := prefByID[ent.ID]
		sources = append(sources, PricingSourcePref{ID: ent.ID, Enabled: ok && pref.Enabled})
	}

	return PricingEffective{
		CostTrackingEnabled: enabled,
		CostTrackingSince:   since,
		Sources:             sources,
	}
}

func (s *PricingStore) defaultEffectiveLocked() PricingEffective {
	sources := make([]PricingSourcePref, 0, len(s.catalog))
	for i, ent := range s.catalog {
		sources = append(sources, PricingSourcePref{ID: ent.ID, Enabled: i == 0})
	}
	return PricingEffective{
		CostTrackingEnabled: true,
		Sources:             sources,
	}
}

// PutGlobal validates and persists the overlay, stamping cost_tracking_since.
func (s *PricingStore) PutGlobal(overlay PricingUserOverlay) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	enabled := overlay.CostTrackingEnabled != nil && *overlay.CostTrackingEnabled
	if err := s.validateOverlayLocked(overlay); err != nil {
		return err
	}

	prevEnabled := s.global != nil && s.global.CostTrackingEnabled != nil && *s.global.CostTrackingEnabled
	var since *time.Time
	switch {
	case enabled && !prevEnabled:
		t := s.now().UTC()
		since = &t
	case enabled && prevEnabled:
		if s.global != nil && s.global.CostTrackingSince != nil {
			t := s.global.CostTrackingSince.UTC()
			since = &t
		} else {
			t := s.now().UTC()
			since = &t
		}
	default:
		since = nil
	}
	overlay.CostTrackingSince = since
	if overlay.CostTrackingEnabled == nil {
		overlay.CostTrackingEnabled = boolPtr(false)
	}

	data, err := yaml.Marshal(overlay)
	if err != nil {
		return err
	}
	if err := writeSettingsFile(s.globalPath, data); err != nil {
		return err
	}
	n := overlay
	s.global = &n
	return nil
}

func (s *PricingStore) validateOverlayLocked(overlay PricingUserOverlay) error {
	allowed := make(map[string]struct{}, len(s.catalog))
	for _, e := range s.catalog {
		allowed[e.ID] = struct{}{}
	}
	seen := map[string]bool{}
	enabledCount := 0
	for _, p := range overlay.Sources {
		if p.ID == "" {
			return fmt.Errorf("pricing source id is required")
		}
		if _, ok := allowed[p.ID]; !ok {
			return fmt.Errorf("%w id %q", ErrPricingUnknownSource, p.ID)
		}
		if seen[p.ID] {
			return fmt.Errorf("duplicate pricing source id %q", p.ID)
		}
		seen[p.ID] = true
		if p.Enabled {
			enabledCount++
		}
	}
	trackingEnabled := overlay.CostTrackingEnabled != nil && *overlay.CostTrackingEnabled
	if trackingEnabled && enabledCount == 0 {
		return ErrPricingNoSource
	}
	if enabledCount > 1 {
		return ErrPricingMultipleSources
	}
	return nil
}

func userPricingPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "pricing.yaml"), nil
}
