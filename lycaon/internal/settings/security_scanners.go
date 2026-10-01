package settings

import (
	"fmt"
	"github.com/lycaon/lycaon/config"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/configdir"
	"gopkg.in/yaml.v3"
)

// ProductLabel is the user-visible Settings / Context name for this capability.
const ProductLabel = "Security scanners"

// SecurityScannersOffDetail returns the shared disabled detail.
func SecurityScannersOffDetail() string {
	return ProductLabel + " is off — turn it on in Settings → " + ProductLabel
}

// SourceVerifyStat reuses hashes while file metadata matches.
const SourceVerifyStat = "stat"

// SourceVerifyContent re-reads every admitted file.
const SourceVerifyContent = "content"

// SecurityScannersConfig is the on-disk security-scanners.yaml shape.
type SecurityScannersConfig struct {
	Enabled           bool   `yaml:"enabled"`
	LandedChangeScope string `yaml:"-"`
	SourceVerify      string `yaml:"-"`
}

// SecurityScannersStore loads bundled defaults and optional global user overlay.
type SecurityScannersStore struct {
	mu         sync.RWMutex
	globalPath string
	bundled    SecurityScannersConfig
	global     *SecurityScannersUserOverlay
	changed    chan struct{}
}

// SecurityScannersUserOverlay is persisted at ~/.config/paintedwolf/security-scanners.yaml.
type SecurityScannersUserOverlay struct {
	Enabled           *bool   `yaml:"enabled,omitempty"`
	LandedChangeScope *string `yaml:"landed_change_scope,omitempty"`
	SourceVerify      *string `yaml:"source_verify,omitempty"`
}

// NewSecurityScannersStore loads Security scanner settings.
func NewSecurityScannersStore() (*SecurityScannersStore, error) {
	globalPath, err := userSecurityScannersPath()
	if err != nil {
		return nil, err
	}
	return NewSecurityScannersStoreAt(globalPath)
}

// NewSecurityScannersStoreAt loads the overlay at globalPath.
func NewSecurityScannersStoreAt(globalPath string) (*SecurityScannersStore, error) {
	s := &SecurityScannersStore{
		globalPath: globalPath,
	}
	if err := s.reloadBundled(); err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(globalPath); err == nil {
		var overlay SecurityScannersUserOverlay
		if err := config.DecodeYAML(data, &overlay); err != nil {
			return nil, fmt.Errorf("parse global Security scanners: %w", err)
		}
		s.global = &overlay
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

func (s *SecurityScannersStore) reloadBundled() error {
	cfg, err := bundledSecurityScanners()
	if err != nil {
		return err
	}
	s.bundled = cfg
	return nil
}

// Effective returns merged Security scanners settings for API and runtime.
func (s *SecurityScannersStore) Effective() SecurityScannersConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	enabled := s.bundled.Enabled
	overlay := s.global
	if overlay != nil && overlay.Enabled != nil {
		enabled = *overlay.Enabled
	}
	return SecurityScannersConfig{
		Enabled:           enabled,
		LandedChangeScope: effectiveLandedChangeScope(overlay),
		SourceVerify:      effectiveSourceVerify(overlay),
	}
}

func effectiveLandedChangeScope(overlay *SecurityScannersUserOverlay) string {
	if overlay != nil && overlay.LandedChangeScope != nil {
		scope := strings.TrimSpace(*overlay.LandedChangeScope)
		switch scope {
		case "path_scoped", "full_root":
			return scope
		}
	}
	return "path_scoped"
}

func effectiveSourceVerify(overlay *SecurityScannersUserOverlay) string {
	if overlay != nil && overlay.SourceVerify != nil {
		if strings.TrimSpace(*overlay.SourceVerify) == SourceVerifyContent {
			return SourceVerifyContent
		}
	}
	return SourceVerifyStat
}

// MergedFrom reports which layers contributed.
func (s *SecurityScannersStore) MergedFrom() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	layers := []string{"bundled"}
	if s.global != nil {
		layers = append(layers, "global")
	} else if _, err := os.Stat(s.globalPath); err == nil {
		layers = append(layers, "global")
	}
	return layers
}

// PutGlobal persists the user overlay.
func (s *SecurityScannersStore) PutGlobal(overlay SecurityScannersUserOverlay) error {
	data, err := yaml.Marshal(overlay)
	if err != nil {
		return err
	}
	if err := writeSettingsFile(s.globalPath, data); err != nil {
		return err
	}
	s.mu.Lock()
	n := overlay
	s.global = &n
	if s.changed != nil {
		close(s.changed)
	}
	s.changed = make(chan struct{})
	s.mu.Unlock()
	return nil
}

// UserOverlay returns a copy of the current global user overlay.
func (s *SecurityScannersStore) UserOverlay() SecurityScannersUserOverlay {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.global == nil {
		return SecurityScannersUserOverlay{}
	}
	return *s.global
}

// Changed closes after a successful settings write. Subscribe before reading
// Effective so a concurrent update cannot be lost between the read and wait.
func (s *SecurityScannersStore) Changed() <-chan struct{} {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changed == nil {
		s.changed = make(chan struct{})
	}
	return s.changed
}

func (s *SecurityScannersStore) OverlayLandedChangeScope() (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.global == nil || s.global.LandedChangeScope == nil {
		return "", false
	}
	scope := strings.TrimSpace(*s.global.LandedChangeScope)
	switch scope {
	case "path_scoped", "full_root":
		return scope, true
	default:
		return "", false
	}
}

func ValidateLandedChangeScope(scope string) string {
	switch strings.TrimSpace(scope) {
	case "path_scoped", "full_root":
		return ""
	default:
		return "landed_change_scope must be path_scoped or full_root"
	}
}

func ValidateSourceVerify(verify string) string {
	switch strings.TrimSpace(verify) {
	case SourceVerifyStat, SourceVerifyContent:
		return ""
	default:
		return "source_verify must be stat or content"
	}
}

func userSecurityScannersPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	// This overlay has device scope.
	return filepath.Join(dir, "security-scanners.yaml"), nil
}

func boolPtr(v bool) *bool { return &v }

// bundledSecurityScanners reads the embedded defaults.
func bundledSecurityScanners() (SecurityScannersConfig, error) {
	data, err := config.Read(config.SecurityScanners)
	if err != nil {
		return SecurityScannersConfig{}, err
	}
	var cfg SecurityScannersConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return SecurityScannersConfig{}, fmt.Errorf("parse bundled config: %w", err)
	}
	return cfg, nil
}
