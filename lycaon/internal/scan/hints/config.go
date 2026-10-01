// Package hints loads scan-hints.yaml and resolves rule ids to agent guidance.
package hints

import (
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/config"
)

// Config is the scan-hints.yaml shape, shared by the bundled defaults and by
// the device and project overlays that layer onto them.
type Config struct {
	Hints       map[string]HintEntry `yaml:"hints"`
	RuleHints   map[string]string    `yaml:"rule_hints"`
	DefaultHint string               `yaml:"default_hint"`
	Scenarios   []ScenarioEntry      `yaml:"scenarios"`
}

// HintEntry is one scan guidance template.
type HintEntry struct {
	Message  string `yaml:"message"`
	Fix      string `yaml:"fix,omitempty"`
	Severity string `yaml:"severity"`
}

// ScenarioEntry is a contract-test fixture for hint resolution.
type ScenarioEntry struct {
	ID      string         `yaml:"id"`
	Finding map[string]any `yaml:"finding"`
	Expect  map[string]any `yaml:"expect"`
}

// LoadBundled returns the shipped scan hints.
func LoadBundled() (*Config, error) {
	data, err := config.Read(config.ScanHints)
	if err != nil {
		return nil, err
	}
	return parse(data)
}

// Load reads one host or project overlay.
func Load(path string) (*Config, error) {
	// #nosec G304 -- path names the selected hints overlay.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parse(data)
}

func parse(data []byte) (*Config, error) {
	var cfg Config
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Hints == nil {
		cfg.Hints = map[string]HintEntry{}
	}
	if cfg.RuleHints == nil {
		cfg.RuleHints = map[string]string{}
	}
	if cfg.DefaultHint == "" {
		cfg.DefaultHint = "SCAN_FINDING_UNMAPPED"
	}
	return &cfg, nil
}

// Merge overlays project hints onto bundled defaults.
func Merge(base, overlay *Config) *Config {
	if base == nil {
		return overlay
	}
	if overlay == nil {
		return base
	}
	out := *base
	out.Hints = map[string]HintEntry{}
	for k, v := range base.Hints {
		out.Hints[k] = v
	}
	for k, v := range overlay.Hints {
		out.Hints[k] = v
	}
	out.RuleHints = map[string]string{}
	for k, v := range base.RuleHints {
		out.RuleHints[k] = v
	}
	for k, v := range overlay.RuleHints {
		out.RuleHints[k] = v
	}
	if overlay.DefaultHint != "" {
		out.DefaultHint = overlay.DefaultHint
	}
	if len(overlay.Scenarios) > 0 {
		out.Scenarios = append([]ScenarioEntry(nil), overlay.Scenarios...)
	}
	return &out
}

// UserScanHintsPath returns the device hint overlay path.
func UserScanHintsPath(configDir string) string {
	return filepath.Join(configDir, "scan-hints.yaml")
}

// LoadMergedForRoots loads bundled hints merged with the device overlay, then each
// project overlay root in order: bundled → device → project. An empty deviceConfigDir
// skips the device layer.
func LoadMergedForRoots(deviceConfigDir string, rootPaths []string) (*Config, error) {
	base, err := LoadBundled()
	if err != nil {
		return nil, err
	}
	merged := base
	if dir := strings.TrimSpace(deviceConfigDir); dir != "" {
		merged, err = mergeOverlayFile(merged, UserScanHintsPath(dir))
		if err != nil {
			return nil, err
		}
	}
	for _, rootPath := range rootPaths {
		rootPath = strings.TrimSpace(rootPath)
		if rootPath == "" {
			continue
		}
		merged, err = mergeOverlayFile(merged, filepath.Join(rootPath, settingsoverlay.DirName(), "scan-hints.yaml"))
		if err != nil {
			return nil, err
		}
	}
	return merged, nil
}

// mergeOverlayFile layers overlayPath onto base, treating a missing file as a no-op.
func mergeOverlayFile(base *Config, overlayPath string) (*Config, error) {
	if _, err := os.Stat(overlayPath); err != nil {
		if os.IsNotExist(err) {
			return base, nil
		}
		return nil, err
	}
	overlay, err := Load(overlayPath)
	if err != nil {
		return nil, err
	}
	return Merge(base, overlay), nil
}
