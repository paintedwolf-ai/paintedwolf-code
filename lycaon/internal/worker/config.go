package worker

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkersConfig configures the local worker poller and executor instance.
type WorkersConfig struct {
	Poller struct {
		MaxConcurrency        int `yaml:"max_concurrency"`
		MaintenanceIntervalMs int `yaml:"maintenance_interval_ms"`
	} `yaml:"poller"`
	Executor struct {
		InstanceID             string `yaml:"instance_id"`
		DefaultExecutionTarget string `yaml:"default_execution_target"`
	} `yaml:"executor"`
}

var (
	bundledWorkersOnce sync.Once
	bundledWorkers     WorkersConfig
	bundledWorkersErr  error
)

// DefaultWorkersConfig returns the shipped workers config.
// Fail-closed: panics if the bundled file cannot be loaded.
func DefaultWorkersConfig() WorkersConfig {
	bundledWorkersOnce.Do(func() {
		bundledWorkers, bundledWorkersErr = LoadWorkersConfig()
	})
	if bundledWorkersErr != nil {
		panic(bundledWorkersErr)
	}
	return bundledWorkers
}

// LoadWorkersConfig reads the shipped workers.yaml. Missing or invalid fails
// closed. Zero fields after unmarshal are filled from spawn caps / local
// execution defaults.
func LoadWorkersConfig() (WorkersConfig, error) {
	data, err := config.Read(config.Workers)
	if err != nil {
		return WorkersConfig{}, fmt.Errorf("read workers config: %w", err)
	}
	var cfg WorkersConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return WorkersConfig{}, fmt.Errorf("parse workers config: %w", err)
	}
	if cfg.Poller.MaxConcurrency <= 0 {
		cfg.Poller.MaxConcurrency = spawn.MaxInFlightTaskWorkers
	}
	if cfg.Poller.MaintenanceIntervalMs <= 0 {
		cfg.Poller.MaintenanceIntervalMs = 5000
	}
	if strings.TrimSpace(cfg.Executor.InstanceID) == "" {
		cfg.Executor.InstanceID = "auto"
	}
	if strings.TrimSpace(cfg.Executor.DefaultExecutionTarget) == "" {
		cfg.Executor.DefaultExecutionTarget = string(api.ExecutionTargetLocal)
	}
	return cfg, nil
}

// ResolveInstanceID returns hostname:pid when cfg says auto.
func ResolveInstanceID(cfg WorkersConfig) string {
	id := strings.TrimSpace(cfg.Executor.InstanceID)
	if id == "" || strings.EqualFold(id, "auto") {
		host, _ := os.Hostname()
		if host == "" {
			host = "local"
		}
		return fmt.Sprintf("%s:%d", host, os.Getpid())
	}
	return id
}

// MaintenanceInterval returns the recovery cadence.
func (c WorkersConfig) MaintenanceInterval() time.Duration {
	return time.Duration(c.Poller.MaintenanceIntervalMs) * time.Millisecond
}
