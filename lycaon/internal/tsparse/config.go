package tsparse

import (
	"fmt"
	"sync"
	"time"

	"github.com/lycaon/lycaon/config"
)

// Purpose selects the configured per-snapshot timeout.
type Purpose string

const (
	Analysis   Purpose = "analysis"
	Validation Purpose = "validation"
)

type Config struct {
	Version             int   `yaml:"version"`
	ValidationTimeoutMS int64 `yaml:"validation_timeout_ms"`
	AnalysisTimeoutMS   int64 `yaml:"analysis_timeout_ms"`
}

var bundledConfig struct {
	sync.Mutex
	generation uint64
	loaded     bool
	value      Config
	err        error
}

// LoadConfig caches immutable sources and reloads mutable configuration.
func LoadConfig() (Config, error) {
	if !config.SourceImmutable() {
		return readConfig()
	}
	bundledConfig.Lock()
	defer bundledConfig.Unlock()
	if !bundledConfig.loaded || bundledConfig.generation != config.SourceGeneration() {
		bundledConfig.value, bundledConfig.err = readConfig()
		bundledConfig.generation = config.SourceGeneration()
		bundledConfig.loaded = true
	}
	return bundledConfig.value, bundledConfig.err
}

func readConfig() (Config, error) {
	raw, err := config.Read(config.SourceParsing)
	if err != nil {
		return Config{}, fmt.Errorf("read source parsing configuration: %w", err)
	}
	var out Config
	if err := config.DecodeYAML(raw, &out); err != nil {
		return Config{}, fmt.Errorf("decode source parsing configuration: %w", err)
	}
	if out.Version != 1 {
		return Config{}, fmt.Errorf("source parsing configuration: unsupported version %d", out.Version)
	}
	for name, value := range map[string]int64{
		"validation_timeout_ms": out.ValidationTimeoutMS,
		"analysis_timeout_ms":   out.AnalysisTimeoutMS,
	} {
		if value <= 0 || value > int64((1<<63-1)/time.Millisecond) {
			return Config{}, fmt.Errorf("source parsing configuration: %s must be a positive duration in milliseconds", name)
		}
	}
	return out, nil
}

func (c Config) timeout(purpose Purpose) (time.Duration, error) {
	switch purpose {
	case Analysis:
		return time.Duration(c.AnalysisTimeoutMS) * time.Millisecond, nil
	case Validation:
		return time.Duration(c.ValidationTimeoutMS) * time.Millisecond, nil
	default:
		return 0, fmt.Errorf("unknown source parsing purpose %q", purpose)
	}
}
