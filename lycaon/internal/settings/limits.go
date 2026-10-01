package settings

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/llm"
	"gopkg.in/yaml.v3"
)

// LimitsStore loads bundled session defaults and optional global/project overlays.
type LimitsStore struct {
	mu           sync.RWMutex
	globalPath   string
	bundled      SessionLimits
	global       *SessionLimits
	projectCache map[string]SessionLimits
}

// NewLimitsStoreAt loads the global overlay at globalPath.
func NewLimitsStoreAt(globalPath string) (*LimitsStore, error) {
	s := &LimitsStore{
		globalPath:   globalPath,
		projectCache: make(map[string]SessionLimits),
	}
	if err := s.reloadBundled(); err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(globalPath); err == nil {
		var lim SessionLimits
		if err := yaml.Unmarshal(data, &lim); err != nil {
			return nil, fmt.Errorf("parse global limits: %w", err)
		}
		lim = NormalizeSessionLimits(lim)
		s.global = &lim
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

// NewLimitsStore loads bundled session.yaml defaults and optional global overlay.
func NewLimitsStore() (*LimitsStore, error) {
	globalPath, err := userLimitsPath()
	if err != nil {
		return nil, err
	}
	return NewLimitsStoreAt(globalPath)
}

func loadLimitsFile(path string) (SessionLimits, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SessionLimits{}, err
	}
	var lim SessionLimits
	if err := yaml.Unmarshal(data, &lim); err != nil {
		return SessionLimits{}, fmt.Errorf("parse limits %s: %w", path, err)
	}
	return lim, nil
}

func (s *LimitsStore) reloadBundled() error {
	s.bundled = DefaultSessionLimits()
	return nil
}

// Get returns effective limits for scope.
func (s *LimitsStore) Get(scope llm.SettingsScope, projectDir string) SessionLimits {
	s.mu.RLock()
	defer s.mu.RUnlock()

	lim := s.bundled
	if s.global != nil {
		lim = MergeSessionLimits(lim, *s.global)
	}
	if scope == llm.SettingsScopeProject && projectDir != "" {
		if cached, ok := s.projectCache[projectDir]; ok {
			lim = MergeSessionLimits(lim, cached)
		} else if proj, err := loadLimitsFile(projectLimitsPath(projectDir)); err == nil {
			lim = MergeSessionLimits(lim, proj)
		}
	}
	return lim
}

// SessionLimits returns merged limits for project scope.
func (s *LimitsStore) SessionLimits(scope llm.SettingsScope, projectDir string) SessionLimits {
	return s.Get(scope, projectDir)
}

// ProjectLimitsAdapter adapts LimitsStore for session.Manager dynamic limits.
type ProjectLimitsAdapter struct {
	Store *LimitsStore
}

// SessionLimits returns merged project-scoped limits.
func (a ProjectLimitsAdapter) SessionLimits(projectDir string) SessionLimits {
	if a.Store == nil {
		return DefaultSessionLimits()
	}
	return a.Store.SessionLimits(llm.SettingsScopeProject, projectDir)
}

// MergedFrom reports which layers contributed.
func (s *LimitsStore) MergedFrom(scope llm.SettingsScope, projectDir string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	layers := []string{"bundled"}
	if s.global != nil {
		layers = append(layers, "global")
	}
	if scope == llm.SettingsScopeProject && projectDir != "" {
		if _, ok := s.projectCache[projectDir]; ok {
			layers = append(layers, "project")
		} else if _, err := os.Stat(projectLimitsPath(projectDir)); err == nil {
			layers = append(layers, "project")
		}
	}
	return layers
}

// PutGlobal persists global user limits overlay.
func (s *LimitsStore) PutGlobal(lim SessionLimits) error {
	lim = NormalizeSessionLimits(lim)
	data, err := yaml.Marshal(lim)
	if err != nil {
		return err
	}
	if err := writeSettingsFile(s.globalPath, data); err != nil {
		return err
	}
	s.mu.Lock()
	n := lim
	s.global = &n
	s.mu.Unlock()
	return nil
}

// PutProject persists project overlay at <overlay>/limits.yaml.
func (s *LimitsStore) PutProject(projectDir string, lim SessionLimits) error {
	path := projectLimitsPath(projectDir)
	if lim == (SessionLimits{}) {
		if err := fseffect.Remove(fseffect.RemoveRequest{Location: fseffect.PathLocation(path)}); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		s.mu.Lock()
		delete(s.projectCache, projectDir)
		s.mu.Unlock()
		return nil
	}
	data, err := yaml.Marshal(lim)
	if err != nil {
		return err
	}
	if err := writeSettingsFile(path, data); err != nil {
		return err
	}
	s.mu.Lock()
	s.projectCache[projectDir] = lim
	s.mu.Unlock()
	return nil
}

// Service bundles approval and limits stores for HTTP handlers.
type Service struct {
	Approvals        *ApprovalStore
	Limits           *LimitsStore
	Review           *ReviewStore
	FileSummaries    *FileSummariesStore
	Power            *PowerStore
	SecurityScanners *SecurityScannersStore
	Pricing          *PricingStore
	TrustSurfaces    *TrustSurfacesStore
	Verify           *VerifyStore
}

// NewService loads device settings.
func NewService() (*Service, error) {
	perms, err := NewApprovalStore()
	if err != nil {
		return nil, fmt.Errorf("approvals: %w", err)
	}
	limits, err := NewLimitsStore()
	if err != nil {
		return nil, fmt.Errorf("limits: %w", err)
	}
	review, err := NewReviewStore()
	if err != nil {
		return nil, fmt.Errorf("review: %w", err)
	}
	fileSummaries, err := NewFileSummariesStore()
	if err != nil {
		return nil, fmt.Errorf("file summaries: %w", err)
	}
	power, err := NewPowerStore()
	if err != nil {
		return nil, fmt.Errorf("power: %w", err)
	}
	security, err := NewSecurityScannersStore()
	if err != nil {
		return nil, fmt.Errorf("security scans: %w", err)
	}
	pricingStore, err := NewPricingStore()
	if err != nil {
		return nil, fmt.Errorf("pricing: %w", err)
	}
	trustSurfaces, err := NewTrustSurfacesStore()
	if err != nil {
		return nil, fmt.Errorf("trust surfaces: %w", err)
	}
	verify, err := NewVerifyStore()
	if err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}
	return &Service{
		Approvals:        perms,
		Limits:           limits,
		Review:           review,
		FileSummaries:    fileSummaries,
		Power:            power,
		SecurityScanners: security,
		Pricing:          pricingStore,
		TrustSurfaces:    trustSurfaces,
		Verify:           verify,
	}, nil
}
