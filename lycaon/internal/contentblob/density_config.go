package contentblob

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/lycaon/lycaon/config"
)

const (
	defaultIdleThresholdDays   = 14
	defaultDensityPollMs       = 21600000 // 6h
	defaultProjectsPerTick     = 25
	defaultBlobsPerProjectTick = 200
)

// DensityConfig controls the idle-project recompression pass.
type DensityConfig struct {
	Density struct {
		IdleThresholdDays   int `yaml:"idle_threshold_days"`
		PollIntervalMs      int `yaml:"poll_interval_ms"`
		ProjectsPerTick     int `yaml:"projects_per_tick"`
		BlobsPerProjectTick int `yaml:"blobs_per_project_tick"`
	} `yaml:"density"`
}

var (
	bundledDensityOnce sync.Once
	bundledDensity     DensityConfig
	bundledDensityErr  error
)

// DefaultDensityConfig returns the bundled density configuration.
func DefaultDensityConfig() DensityConfig {
	bundledDensityOnce.Do(func() {
		bundledDensity, bundledDensityErr = bundledDensityConfig()
	})
	if bundledDensityErr != nil {
		panic(bundledDensityErr)
	}
	return bundledDensity
}

// LoadDensityOverlay applies a host overlay to the bundled defaults.
func LoadDensityOverlay(path string) (DensityConfig, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- caller-provided overlay path
	if err != nil {
		return DensityConfig{}, fmt.Errorf("read storage density overlay: %w", err)
	}
	return decodeDensityConfig(raw, DefaultDensityConfig())
}

func bundledDensityConfig() (DensityConfig, error) {
	data, err := config.Read(config.StorageDensity)
	if err != nil {
		return DensityConfig{}, fmt.Errorf("read storage density config: %w", err)
	}
	return decodeDensityConfig(data, DensityConfig{})
}

func decodeDensityConfig(data []byte, base DensityConfig) (DensityConfig, error) {
	cfg := base
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return DensityConfig{}, fmt.Errorf("parse storage density config: %w", err)
	}
	if cfg.Density.IdleThresholdDays <= 0 {
		cfg.Density.IdleThresholdDays = defaultIdleThresholdDays
	}
	if cfg.Density.PollIntervalMs <= 0 {
		cfg.Density.PollIntervalMs = defaultDensityPollMs
	}
	if cfg.Density.ProjectsPerTick <= 0 {
		cfg.Density.ProjectsPerTick = defaultProjectsPerTick
	}
	if cfg.Density.BlobsPerProjectTick <= 0 {
		cfg.Density.BlobsPerProjectTick = defaultBlobsPerProjectTick
	}
	return cfg, nil
}

// IdleThreshold is how long a project must sit unopened before it is a
// density-pass candidate.
func (c DensityConfig) IdleThreshold() time.Duration {
	days := c.Density.IdleThresholdDays
	if days <= 0 {
		days = defaultIdleThresholdDays
	}
	return time.Duration(days) * 24 * time.Hour
}

// PollInterval is the density-pass ticker period.
func (c DensityConfig) PollInterval() time.Duration {
	ms := c.Density.PollIntervalMs
	if ms <= 0 {
		ms = defaultDensityPollMs
	}
	return time.Duration(ms) * time.Millisecond
}

// ProjectsPerTick bounds how many idle projects one tick considers.
func (c DensityConfig) ProjectsPerTick() int {
	if c.Density.ProjectsPerTick <= 0 {
		return defaultProjectsPerTick
	}
	return c.Density.ProjectsPerTick
}

// BlobsPerProjectTick bounds how many hot objects one tick recompresses per project.
func (c DensityConfig) BlobsPerProjectTick() int {
	if c.Density.BlobsPerProjectTick <= 0 {
		return defaultBlobsPerProjectTick
	}
	return c.Density.BlobsPerProjectTick
}
