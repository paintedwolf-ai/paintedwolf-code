package settings

import (
	"fmt"
	"github.com/lycaon/lycaon/config"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/pkg/pathglob"
	"gopkg.in/yaml.v3"
)

// ContentReviewRule matches edits by path and optional tool.
// DisabledUntil pauses this rule until the specified time.
type ContentReviewRule struct {
	Tool          string     `yaml:"tool,omitempty" json:"tool,omitempty"`
	Path          string     `yaml:"path" json:"path"`
	Reason        string     `yaml:"reason,omitempty" json:"reason,omitempty"`
	DisabledUntil *time.Time `yaml:"disabled_until,omitempty" json:"disabled_until,omitempty"`
}

// ReviewConfig selects edits that require content review within a scope.
type ReviewConfig struct {
	ReviewPaths []ContentReviewRule `yaml:"review_paths" json:"review_paths"`
}

// ReviewStore loads bundled, global, and project review overlays.
type ReviewStore struct {
	mu           sync.RWMutex
	globalPath   string
	bundled      ReviewConfig
	global       *ReviewConfig
	projectCache map[string]ReviewConfig
}

// NewReviewStoreAt loads the global overlay at globalPath.
func NewReviewStoreAt(globalPath string) (*ReviewStore, error) {
	s := &ReviewStore{
		globalPath:   globalPath,
		projectCache: make(map[string]ReviewConfig),
	}
	if err := s.reloadBundled(); err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(globalPath); err == nil {
		var cfg ReviewConfig
		if err := config.DecodeYAML(data, &cfg); err != nil {
			return nil, err
		}
		cfg = normalizeReviewConfig(cfg)
		s.global = &cfg
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

// NewReviewStore loads bundled review defaults and optional global overlay.
func NewReviewStore() (*ReviewStore, error) {
	globalPath, err := userReviewPath()
	if err != nil {
		return nil, err
	}
	return NewReviewStoreAt(globalPath)
}

func (s *ReviewStore) reloadBundled() error {
	cfg, err := bundledReviewConfig()
	if err != nil {
		if os.IsNotExist(err) {
			s.bundled = ReviewConfig{}
			return nil
		}
		return err
	}
	s.bundled = cfg
	return nil
}

func loadReviewFile(path string) (ReviewConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ReviewConfig{}, err
	}
	var cfg ReviewConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return ReviewConfig{}, err
	}
	return normalizeReviewConfig(cfg), nil
}

func normalizeReviewConfig(cfg ReviewConfig) ReviewConfig {
	out := make([]ContentReviewRule, 0, len(cfg.ReviewPaths))
	for _, r := range cfg.ReviewPaths {
		r.Tool = strings.TrimSpace(r.Tool)
		r.Path = strings.TrimSpace(r.Path)
		if r.Path == "" {
			continue // a review rule must name a path glob
		}
		out = append(out, r)
	}
	cfg.ReviewPaths = out
	return cfg
}

// Get returns effective review config for scope.
func (s *ReviewStore) Get(scope llm.SettingsScope, projectDir string) ReviewConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg := s.bundled
	if s.global != nil {
		cfg = mergeReviewConfig(cfg, *s.global)
	}
	if scope == llm.SettingsScopeProject && projectDir != "" {
		if cached, ok := s.projectCache[projectDir]; ok {
			cfg = mergeReviewConfig(cfg, cached)
		} else if proj, err := loadReviewFile(projectReviewPath(projectDir)); err == nil {
			cfg = mergeReviewConfig(cfg, proj)
		}
	}
	return cfg
}

func mergeReviewConfig(base, overlay ReviewConfig) ReviewConfig {
	out := base
	if len(overlay.ReviewPaths) > 0 {
		out.ReviewPaths = append(append([]ContentReviewRule(nil), out.ReviewPaths...), overlay.ReviewPaths...)
	}
	return normalizeReviewConfig(out)
}

// MergedFrom reports which layers contributed.
func (s *ReviewStore) MergedFrom(scope llm.SettingsScope, projectDir string) []string {
	var layers []string
	s.mu.RLock()
	defer s.mu.RUnlock()
	layers = append(layers, "bundled")
	if s.global != nil {
		layers = append(layers, "global")
	}
	if scope == llm.SettingsScopeProject && projectDir != "" {
		if _, ok := s.projectCache[projectDir]; ok {
			layers = append(layers, "project")
		} else if _, err := os.Stat(projectReviewPath(projectDir)); err == nil {
			layers = append(layers, "project")
		}
	}
	return layers
}

// PutGlobal persists global user review overlay.
func (s *ReviewStore) PutGlobal(cfg ReviewConfig) error {
	cfg = normalizeReviewConfig(cfg)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := writeSettingsFile(s.globalPath, data); err != nil {
		return err
	}
	s.mu.Lock()
	copied := cfg
	s.global = &copied
	s.mu.Unlock()
	return nil
}

// PutProject persists project overlay at <overlay>/review.yaml.
func (s *ReviewStore) PutProject(projectDir string, cfg ReviewConfig) error {
	cfg = normalizeReviewConfig(cfg)
	path := projectReviewPath(projectDir)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := writeSettingsFile(path, data); err != nil {
		return err
	}
	s.mu.Lock()
	s.projectCache[projectDir] = cfg
	s.mu.Unlock()
	return nil
}

// MatchesReviewPath reports whether tool/path requires human edit review.
// A rule paused by DisabledUntil is inert until that instant passes.
func (cfg ReviewConfig) MatchesReviewPath(tool, path string) bool {
	now := time.Now()
	for _, r := range cfg.ReviewPaths {
		if r.Tool != "" && r.Tool != tool {
			continue
		}
		if r.DisabledUntil != nil && r.DisabledUntil.After(now) {
			continue
		}
		if reviewPathCovers(r.Path, path) {
			return true
		}
	}
	return false
}

// reviewPathCovers reports whether a review rule's path glob covers path. A bare
// `*` or `**` names the whole tree; everything else is repo-relative coverage,
// where a plain directory names its descendants. An empty pattern cannot arrive
// here — normalizeReviewConfig drops a rule that names no path.
func reviewPathCovers(pattern, path string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "*" || pattern == "**" {
		return true
	}
	return pathglob.Covers(pattern, path)
}

func userReviewPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, settingsoverlay.BasenameReview), nil
}

func projectReviewPath(projectDir string) string {
	return settingsoverlay.ProjectOverlayPath(projectDir, settingsoverlay.BasenameReview)
}

// bundledReviewConfig reads the embedded defaults.
func bundledReviewConfig() (ReviewConfig, error) {
	data, err := config.Read(config.Review)
	if err != nil {
		return ReviewConfig{}, err
	}
	var cfg ReviewConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return ReviewConfig{}, fmt.Errorf("parse bundled config: %w", err)
	}
	return cfg, nil
}
