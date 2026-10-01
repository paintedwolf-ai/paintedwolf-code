// Package toolscope holds catalog thresholds for root-scope tool guards.
package toolscope

import (
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/config"
)

// Config is the shipped tool-scope configuration.
type Config struct {
	Version                 int `yaml:"version"`
	RootStructuralFileCount int `yaml:"root_structural_file_count"`
}

// Default returns the bundled tool-scope config.
func Default() Config {
	cfg, err := Load()
	if err != nil {
		panic(err)
	}
	return cfg
}

// Load reads config/packs/painted-wolf/platform/host/tools-scope.yaml from the shipped catalog.
func Load() (Config, error) {
	raw, err := config.Read(config.ToolsScope)
	if err != nil {
		return Config{}, fmt.Errorf("read tools-scope: %w", err)
	}
	var cfg Config
	if err := config.DecodeYAML(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse tools-scope: %w", err)
	}
	if cfg.Version <= 0 {
		return Config{}, fmt.Errorf("tools-scope: version must be positive")
	}
	if cfg.RootStructuralFileCount <= 0 {
		return Config{}, fmt.Errorf("tools-scope: root_structural_file_count must be positive")
	}
	return cfg, nil
}

var (
	globalMu sync.RWMutex
	global   Config
)

// SetGlobal installs the process-wide scope thresholds (serve wiring).
func SetGlobal(cfg Config) {
	globalMu.Lock()
	defer globalMu.Unlock()
	global = cfg
}

// Global returns the process-wide scope thresholds.
func Global() Config {
	globalMu.RLock()
	g := global
	globalMu.RUnlock()
	if g.Version > 0 {
		return g
	}
	globalMu.Lock()
	defer globalMu.Unlock()
	if global.Version == 0 {
		global = Default()
	}
	return global
}

// NeedsRootScopeGuard selects structural handling for an open root.
func NeedsRootScopeGuard(fileCount int, known bool, threshold int) bool {
	if threshold <= 0 {
		threshold = Global().RootStructuralFileCount
	}
	if !known {
		return true
	}
	return fileCount >= threshold
}
