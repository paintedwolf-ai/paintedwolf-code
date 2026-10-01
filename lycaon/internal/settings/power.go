package settings

import (
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

// PowerStore stores the device power preference.
type PowerStore struct {
	mu        sync.RWMutex
	path      string
	keepAwake bool
}

type powerConfig struct {
	KeepAwakeWhileWorking *bool `yaml:"keep_awake_while_working,omitempty"`
}

// NewPowerStore loads the device preference.
func NewPowerStore() (*PowerStore, error) {
	path, err := userPowerPath()
	if err != nil {
		return nil, err
	}
	return OpenPowerStore(path)
}

// OpenPowerStore loads the preference at path.
func OpenPowerStore(path string) (*PowerStore, error) {
	store := &PowerStore{path: path, keepAwake: true}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg powerConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse power settings: %w", err)
	}
	if cfg.KeepAwakeWhileWorking != nil {
		store.keepAwake = *cfg.KeepAwakeWhileWorking
	}
	return store, nil
}

// KeepAwakeWhileWorking reports the persisted device preference.
func (s *PowerStore) KeepAwakeWhileWorking() bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.keepAwake
}

// PutKeepAwakeWhileWorking persists the device preference.
func (s *PowerStore) PutKeepAwakeWhileWorking(enabled bool) error {
	if s == nil {
		return fmt.Errorf("power settings are not configured")
	}
	data, err := yaml.Marshal(powerConfig{KeepAwakeWhileWorking: &enabled})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeSettingsFile(s.path, data); err != nil {
		return err
	}
	s.keepAwake = enabled
	return nil
}
