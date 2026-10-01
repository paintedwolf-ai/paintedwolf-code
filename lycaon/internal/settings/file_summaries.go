package settings

import (
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

// FileSummariesStore stores the device-wide File summaries switch.
type FileSummariesStore struct {
	mu      sync.RWMutex
	path    string
	enabled bool
}

type fileSummariesConfig struct {
	Enabled *bool `yaml:"enabled,omitempty"`
}

// NewFileSummariesStore loads the device setting. File summaries default on.
func NewFileSummariesStore() (*FileSummariesStore, error) {
	path, err := userFileSummariesPath()
	if err != nil {
		return nil, err
	}
	return NewFileSummariesStoreAt(path)
}

// NewFileSummariesStoreAt loads the setting at path.
func NewFileSummariesStoreAt(path string) (*FileSummariesStore, error) {
	store := &FileSummariesStore{path: path, enabled: true}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg fileSummariesConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse File summaries settings: %w", err)
	}
	if cfg.Enabled != nil {
		store.enabled = *cfg.Enabled
	}
	return store, nil
}

// Enabled reports whether briefing endpoints may do work.
func (s *FileSummariesStore) Enabled() bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled
}

// PutEnabled persists the device switch.
func (s *FileSummariesStore) PutEnabled(enabled bool) error {
	if s == nil {
		return fmt.Errorf("file summaries settings are not configured")
	}
	data, err := yaml.Marshal(fileSummariesConfig{Enabled: &enabled})
	if err != nil {
		return err
	}
	if err := writeSettingsFile(s.path, data); err != nil {
		return err
	}
	s.mu.Lock()
	s.enabled = enabled
	s.mu.Unlock()
	return nil
}
