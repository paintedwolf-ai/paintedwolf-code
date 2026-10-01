// Package branchretention bounds the branch trees completed write workers
// leave behind. A sealed overlay makes its tree rebuildable, so the tree is
// held as a cache: kept while it is being used, reclaimed by idle age and by a
// device-wide byte budget, and rebuilt on the next read.
package branchretention

import (
	"fmt"
	"sync"
	"time"

	"github.com/lycaon/lycaon/config"
)

const (
	defaultIdleAge       = 7 * 24 * time.Hour
	defaultBudgetBytes   = 4 << 30
	defaultSweepInterval = time.Hour
)

// Config controls worker branch retention.
type Config struct {
	// IdleAge reclaims a sealed tree nothing has used for this long.
	IdleAge time.Duration
	// BudgetBytes bounds the allocated size of every branch tree on the device;
	// the least recently used sealed trees go first.
	BudgetBytes int64
	// SweepInterval paces the background sweep.
	SweepInterval time.Duration
}

type configFile struct {
	WorkerBranches struct {
		IdleDays    int   `yaml:"idle_days"`
		BudgetBytes int64 `yaml:"budget_bytes"`
		SweepMS     int   `yaml:"sweep_interval_ms"`
	} `yaml:"worker_branches"`
}

var (
	defaultOnce sync.Once
	defaultCfg  Config
	defaultErr  error
)

// DefaultConfig is the bundled policy.
func DefaultConfig() Config {
	defaultOnce.Do(func() {
		raw, err := config.Read(config.StorageWorkerBranch)
		if err != nil {
			defaultErr = fmt.Errorf("branch retention: read config: %w", err)
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
		return Config{}, fmt.Errorf("branch retention: parse config: %w", err)
	}
	out := Config{
		IdleAge:       time.Duration(file.WorkerBranches.IdleDays) * 24 * time.Hour,
		BudgetBytes:   file.WorkerBranches.BudgetBytes,
		SweepInterval: time.Duration(file.WorkerBranches.SweepMS) * time.Millisecond,
	}
	return out.normalized(), nil
}

func (c Config) normalized() Config {
	if c.IdleAge <= 0 {
		c.IdleAge = defaultIdleAge
	}
	if c.BudgetBytes <= 0 {
		c.BudgetBytes = defaultBudgetBytes
	}
	if c.SweepInterval <= 0 {
		c.SweepInterval = defaultSweepInterval
	}
	return c
}
