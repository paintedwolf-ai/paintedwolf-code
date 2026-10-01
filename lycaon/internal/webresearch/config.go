package webresearch

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/fseffect"
	"gopkg.in/yaml.v3"
)

const (
	configFileMode    = 0o600
	webResearchConfig = "web-research-config.yaml"
)

type configFile struct {
	SearchEnabled     *bool                        `yaml:"search_enabled,omitempty"`
	EnabledProviders  *[]string                    `yaml:"search_providers,omitempty"`
	ProviderOverrides map[string]map[string]string `yaml:"providers"`
	Warming           *bool                        `yaml:"warming,omitempty"`
	GuessDomains      *bool                        `yaml:"guess_domains,omitempty"`
	WarmingCaps       warmingCapsFile              `yaml:"warming_caps,omitempty"`
}

type warmingCapsFile struct {
	SeedWarmsPerHour int `yaml:"seed_warms_per_hour,omitempty"`
	TurnHosts        int `yaml:"turn_hosts,omitempty"`
	TurnProbes       int `yaml:"turn_probes,omitempty"`
	SeedProbes       int `yaml:"seed_probes,omitempty"`
	ScheduledProbes  int `yaml:"scheduled_probes,omitempty"`
}

// WarmingMode controls background crawling and model seeding.
type WarmingMode string

const (
	WarmingOff       WarmingMode = "off"
	WarmingCrawlOnly WarmingMode = "crawl_only"
	WarmingFull      WarmingMode = "full"
)

// WarmCaps are the background-warming budgets, enforced by the Warmer.
type WarmCaps struct {
	SeedWarmsPerHour int
	TurnHosts        int
	TurnProbes       int
	SeedProbes       int
	ScheduledProbes  int
}

func defaultWarmCaps() WarmCaps {
	return WarmCaps{SeedWarmsPerHour: 8, TurnHosts: 3, TurnProbes: 4, SeedProbes: 8, ScheduledProbes: 24}
}

// ConfigStore persists web research prefs at ~/.config/paintedwolf/web-research-config.yaml.
type ConfigStore struct {
	mu   sync.RWMutex
	path string
	raw  configFile
}

// NewConfigStore loads prefs from the default user path.
func NewConfigStore() (*ConfigStore, error) {
	path, err := userConfigPath(webResearchConfig)
	if err != nil {
		return nil, err
	}
	s := &ConfigStore{path: path}
	if err := s.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

// NewConfigStoreAt is for tests.
func NewConfigStoreAt(path string) *ConfigStore {
	s := &ConfigStore{path: path}
	if err := s.load(); err != nil && !os.IsNotExist(err) {
		s.raw.ProviderOverrides = make(map[string]map[string]string)
	}
	return s
}

func userConfigPath(name string) (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func (s *ConfigStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var raw configFile
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse web research config: %w", err)
	}
	if raw.ProviderOverrides == nil {
		raw.ProviderOverrides = make(map[string]map[string]string)
	}
	s.raw = raw
	return nil
}

func (s *ConfigStore) save(raw configFile) error {
	data, err := yaml.Marshal(raw)
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(s.path),
		Source:   bytes.NewReader(data),
		Mode:     configFileMode,
		DirMode:  0o700,
	})
	return err
}

func cloneConfigFile(src configFile) configFile {
	out := src
	if src.SearchEnabled != nil {
		v := *src.SearchEnabled
		out.SearchEnabled = &v
	}
	if src.Warming != nil {
		v := *src.Warming
		out.Warming = &v
	}
	if src.GuessDomains != nil {
		v := *src.GuessDomains
		out.GuessDomains = &v
	}
	if src.EnabledProviders != nil {
		providers := append([]string(nil), (*src.EnabledProviders)...)
		out.EnabledProviders = &providers
	}
	out.ProviderOverrides = make(map[string]map[string]string, len(src.ProviderOverrides))
	for id, fields := range src.ProviderOverrides {
		copied := make(map[string]string, len(fields))
		for key, value := range fields {
			copied[key] = value
		}
		out.ProviderOverrides[id] = copied
	}
	return out
}

// ProviderSelection returns the saved provider set and whether it is explicit.
func (s *ConfigStore) ProviderSelection() ([]string, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.raw.EnabledProviders == nil {
		return nil, false
	}
	return append([]string(nil), (*s.raw.EnabledProviders)...), true
}

// ProviderConfig returns stored extra fields for one provider id.
func (s *ConfigStore) ProviderConfig(id string) map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.raw.ProviderOverrides == nil {
		return nil
	}
	src := s.raw.ProviderOverrides[id]
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// SearchEnabled reports whether web_search is allowed (true when unset).
func (s *ConfigStore) SearchEnabled() bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.raw.SearchEnabled == nil {
		return true
	}
	return *s.raw.SearchEnabled
}

// ApplyPrefs updates warming, guess-domains, search enablement, and/or
// provider enablement.
func (s *ConfigStore) ApplyPrefs(warming *bool, guessDomains *bool, searchEnabled *bool, enabled []string) error {
	if s == nil {
		return fmt.Errorf("config store required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneConfigFile(s.raw)
	if warming != nil {
		v := *warming
		next.Warming = &v
		if !v {
			off := false
			next.GuessDomains = &off
		}
	}
	if guessDomains != nil {
		v := *guessDomains
		next.GuessDomains = &v
		if v {
			on := true
			next.Warming = &on
		}
	}
	if searchEnabled != nil {
		enabledCopy := *searchEnabled
		next.SearchEnabled = &enabledCopy
	}
	if enabled != nil {
		providers := append([]string(nil), enabled...)
		next.EnabledProviders = &providers
	}
	if err := s.save(next); err != nil {
		return err
	}
	s.raw = next
	return nil
}

// WarmingEnabled defaults on for a loaded store and off for a nil store.
func (s *ConfigStore) WarmingEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.raw.Warming == nil {
		return true
	}
	return *s.raw.Warming
}

// GuessDomains defaults on when warming is enabled.
func (s *ConfigStore) GuessDomains() bool {
	if s == nil || !s.WarmingEnabled() {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.raw.GuessDomains == nil {
		return true
	}
	return *s.raw.GuessDomains
}

// Warming returns the derived warmer gate for crawl vs model-seed paths.
func (s *ConfigStore) Warming() WarmingMode {
	if !s.WarmingEnabled() {
		return WarmingOff
	}
	if s.GuessDomains() {
		return WarmingFull
	}
	return WarmingCrawlOnly
}

// WarmingCaps returns configured warming budgets with defaults filled in.
func (s *ConfigStore) WarmingCaps() WarmCaps {
	caps := defaultWarmCaps()
	if s == nil {
		return caps
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	f := s.raw.WarmingCaps
	if f.SeedWarmsPerHour > 0 {
		caps.SeedWarmsPerHour = f.SeedWarmsPerHour
	}
	if f.TurnHosts > 0 {
		caps.TurnHosts = f.TurnHosts
	}
	if f.TurnProbes > 0 {
		caps.TurnProbes = f.TurnProbes
	}
	if f.SeedProbes > 0 {
		caps.SeedProbes = f.SeedProbes
	}
	if f.ScheduledProbes > 0 {
		caps.ScheduledProbes = f.ScheduledProbes
	}
	return caps
}

// SetProviderConfig persists extra fields / endpoint for one catalog provider id.
func (s *ConfigStore) SetProviderConfig(id string, fields map[string]string) error {
	if s == nil {
		return fmt.Errorf("config store required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneConfigFile(s.raw)
	if next.ProviderOverrides == nil {
		next.ProviderOverrides = make(map[string]map[string]string)
	}
	if next.ProviderOverrides[id] == nil {
		next.ProviderOverrides[id] = make(map[string]string)
	}
	for k, v := range fields {
		if v == "" {
			delete(next.ProviderOverrides[id], k)
			continue
		}
		next.ProviderOverrides[id][k] = v
	}
	if len(next.ProviderOverrides[id]) == 0 {
		delete(next.ProviderOverrides, id)
	}
	if err := s.save(next); err != nil {
		return err
	}
	s.raw = next
	return nil
}
