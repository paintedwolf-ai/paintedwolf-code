// Package debugretention bounds diagnostic files and capture sessions.
package debugretention

import (
	"fmt"
	"sync"
	"time"

	"github.com/lycaon/lycaon/config"
)

const (
	defaultMaxFileBytes  = 128 << 20
	defaultMaxSessions   = 20
	defaultMaxTotalBytes = 2 << 30
	defaultSweepInterval = time.Hour
)

// Config controls diagnostic file and capture-session retention.
type Config struct {
	MaxFileBytes  int64
	MaxSessions   int
	MaxTotalBytes int64
	SweepInterval time.Duration
}

type configFile struct {
	Debug struct {
		MaxFileBytes  int64 `yaml:"max_file_bytes"`
		MaxSessions   int   `yaml:"max_sessions"`
		MaxTotalBytes int64 `yaml:"max_total_bytes"`
		SweepMS       int   `yaml:"sweep_interval_ms"`
	} `yaml:"debug"`
}

var (
	defaultOnce sync.Once
	defaultCfg  Config
	defaultErr  error
)

// DefaultConfig returns the bundled diagnostic retention policy.
func DefaultConfig() Config {
	defaultOnce.Do(func() {
		raw, err := config.Read(config.StorageDebug)
		if err != nil {
			defaultErr = fmt.Errorf("debug retention: read config: %w", err)
			return
		}
		defaultCfg, defaultErr = decodeConfig(raw)
	})
	if defaultErr != nil {
		panic(defaultErr)
	}
	return defaultCfg
}

func decodeConfig(raw []byte) (Config, error) {
	var file configFile
	if err := config.DecodeYAML(raw, &file); err != nil {
		return Config{}, fmt.Errorf("debug retention: parse config: %w", err)
	}
	out := Config{
		MaxFileBytes:  file.Debug.MaxFileBytes,
		MaxSessions:   file.Debug.MaxSessions,
		MaxTotalBytes: file.Debug.MaxTotalBytes,
		SweepInterval: time.Duration(file.Debug.SweepMS) * time.Millisecond,
	}
	return out.normalized(), nil
}

func (cfg Config) normalized() Config {
	if cfg.MaxFileBytes <= 0 {
		cfg.MaxFileBytes = defaultMaxFileBytes
	}
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = defaultMaxSessions
	}
	if cfg.MaxTotalBytes <= 0 {
		cfg.MaxTotalBytes = defaultMaxTotalBytes
	}
	if cfg.SweepInterval <= 0 {
		cfg.SweepInterval = defaultSweepInterval
	}
	return cfg
}
