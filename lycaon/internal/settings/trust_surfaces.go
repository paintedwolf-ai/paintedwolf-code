package settings

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"gopkg.in/yaml.v3"
)

const trustSurfacesFileName = "trust-surfaces.yaml"

// TrustSurfacesFile is the on-disk device overlay shape.
type TrustSurfacesFile struct {
	Enabled map[string]bool `yaml:"enabled,omitempty"`
}

// TrustSurfacesStore holds device trust switches.
type TrustSurfacesStore struct {
	mu      sync.RWMutex
	path    string
	enabled map[string]bool
}

// NewTrustSurfacesStore loads the device overlay (defaults all enabled).
func NewTrustSurfacesStore() (*TrustSurfacesStore, error) {
	path, err := userTrustSurfacesPath()
	if err != nil {
		return nil, err
	}
	return NewTrustSurfacesStoreAt(path)
}

// NewTrustSurfacesStoreAt loads the overlay at path.
func NewTrustSurfacesStoreAt(path string) (*TrustSurfacesStore, error) {
	s := &TrustSurfacesStore{
		path:    path,
		enabled: defaultTrustEnabled(),
	}
	if data, err := os.ReadFile(path); err == nil {
		var file TrustSurfacesFile
		if err := yaml.Unmarshal(data, &file); err != nil {
			return nil, fmt.Errorf("parse trust surfaces: %w", err)
		}
		if err := ValidateTrustSwitchIds(file.Enabled); err != nil {
			return nil, err
		}
		for id, on := range file.Enabled {
			s.enabled[id] = on
		}
		return s, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := s.persistDefaults(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *TrustSurfacesStore) persistDefaults() error {
	file := TrustSurfacesFile{Enabled: cloneEnabledMap(s.enabled)}
	data, err := yaml.Marshal(file)
	if err != nil {
		return err
	}
	return writeSettingsFile(s.path, data)
}

func defaultTrustEnabled() map[string]bool {
	rows := projectcontrib.Registry()
	out := make(map[string]bool, len(rows))
	for _, row := range rows {
		out[row.ID] = true
	}
	return out
}

func cloneEnabledMap(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// DeviceEnabled reports whether a surface is on at the device level.
func (s *TrustSurfacesStore) DeviceEnabled(id string) bool {
	_, ok := projectcontrib.Lookup(id)
	if !ok {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.enabled[id]; ok {
		return v
	}
	return true
}

// Applies reports whether a surface affects the project.
func (s *TrustSurfacesStore) Applies(id string, p project.Project) bool {
	return s.DeviceEnabled(id) && project.TrustEnabled(p, id)
}

// PutEnabled merges partial device toggles and persists the overlay.
func (s *TrustSurfacesStore) PutEnabled(updates map[string]bool) error {
	if err := ValidateTrustSwitchIds(updates); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	enabled := cloneEnabledMap(s.enabled)
	for id, on := range updates {
		enabled[id] = on
	}
	data, err := yaml.Marshal(TrustSurfacesFile{Enabled: enabled})
	if err != nil {
		return err
	}
	if err := writeSettingsFile(s.path, data); err != nil {
		return err
	}
	s.enabled = enabled
	return nil
}

func userTrustSurfacesPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, trustSurfacesFileName), nil
}
