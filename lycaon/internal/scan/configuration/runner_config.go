package configuration

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

// RunnerConfig configures the event-driven scan runner.
type RunnerConfig struct {
	Runner struct {
		MaxConcurrency      int    `yaml:"max_concurrency"`
		ReconcileIntervalMs int    `yaml:"reconcile_interval_ms"`
		RetryDelayMs        int    `yaml:"retry_delay_ms"`
		ProcessPriority     string `yaml:"process_priority"`
		ResultSpillBytes    int    `yaml:"result_spill_bytes"`
		// ChunkFiles caps each engine invocation. Completed chunks persist until
		// scan completion so interrupted scans can resume.
		ChunkFiles int `yaml:"chunk_files"`
		// FileTimeoutMs bounds one file inside an invocation for engines that
		// read files one at a time; an overrun is a target_unscanned warning.
		FileTimeoutMs int `yaml:"file_timeout_ms"`
	} `yaml:"runner"`
}

// LandedChangeScope selects the scan width for landed changes.
type LandedChangeScope string

const (
	LandedChangeScopePathScoped LandedChangeScope = "path_scoped"
	LandedChangeScopeFullRoot   LandedChangeScope = "full_root"
)

type LandedChangeConfig struct {
	Enabled bool              `yaml:"enabled"`
	Scope   LandedChangeScope `yaml:"scope"`
}

const (
	defaultRefreshSettleMs    = 45000
	defaultMaxDeferMs         = 900000
	defaultWriteBurstSettleMs = 4000
	defaultCadenceReconcileMs = 60000
	DefaultDriftMinPaths      = 8
	DefaultDriftRatio         = 0.25
)

// CadenceConfig controls bootstrap, write bursts, and drift refreshes.
type CadenceConfig struct {
	RefreshSettleMs     int     `yaml:"refresh_settle_ms"`
	MaxDeferMs          int     `yaml:"max_defer_ms"`
	WriteBurstSettleMs  int     `yaml:"write_burst_settle_ms"`
	ReconcileIntervalMs int     `yaml:"reconcile_interval_ms"`
	DriftMinPaths       int     `yaml:"drift_min_paths"`
	DriftRatio          float64 `yaml:"drift_ratio"`
}

// GatesConfig configures proactive scan triggers and agent feedback budget.
type GatesConfig struct {
	Gates struct {
		ProactiveCategories []api.ScanCategory `yaml:"proactive_categories"`
		LandedChange        LandedChangeConfig `yaml:"landed_change"`
		Cadence             CadenceConfig      `yaml:"cadence"`
		AgentBudget         AgentBudgetConfig  `yaml:"agent_budget"`
		BlockOn             []string           `yaml:"block_on"`
	} `yaml:"gates"`
}

var (
	bundledRunnerOnce sync.Once
	bundledRunner     RunnerConfig
	bundledRunnerErr  error

	bundledGatesOnce sync.Once
	bundledGates     GatesConfig
	bundledGatesErr  error
)

// DefaultRunnerConfig returns the bundled runner configuration.
func DefaultRunnerConfig() RunnerConfig {
	bundledRunnerOnce.Do(func() {
		bundledRunner, bundledRunnerErr = bundledRunnerConfig()
	})
	if bundledRunnerErr != nil {
		panic(bundledRunnerErr)
	}
	return bundledRunner
}

// DefaultGatesConfig returns the bundled gates configuration.
func DefaultGatesConfig() GatesConfig {
	bundledGatesOnce.Do(func() {
		bundledGates, bundledGatesErr = bundledGatesConfig()
	})
	if bundledGatesErr != nil {
		panic(bundledGatesErr)
	}
	return bundledGates
}

// DefaultAgentBudget returns agent_budget from bundled gates.yaml.
func DefaultAgentBudget() AgentBudgetConfig {
	return DefaultGatesConfig().Gates.AgentBudget
}

func bundledRunnerConfig() (RunnerConfig, error) {
	data, err := config.Read(config.ScannerRunner)
	if err != nil {
		return RunnerConfig{}, fmt.Errorf("read scan runner config: %w", err)
	}
	return decodeRunnerConfig(data)
}

func decodeRunnerConfig(data []byte) (RunnerConfig, error) {
	var cfg RunnerConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return RunnerConfig{}, fmt.Errorf("parse scan runner config: %w", err)
	}
	normalizeRunnerConfig(&cfg)
	if cfg.Runner.MaxConcurrency <= 0 {
		return RunnerConfig{}, fmt.Errorf("scan runner config: max_concurrency must be positive")
	}
	if cfg.Runner.ReconcileIntervalMs <= 0 {
		return RunnerConfig{}, fmt.Errorf("scan runner config: reconcile_interval_ms must be positive")
	}
	if cfg.Runner.RetryDelayMs <= 0 {
		return RunnerConfig{}, fmt.Errorf("scan runner config: retry_delay_ms must be positive")
	}
	if cfg.Runner.ResultSpillBytes <= 0 {
		return RunnerConfig{}, fmt.Errorf("scan runner config: result_spill_bytes must be positive")
	}
	if cfg.Runner.ChunkFiles < 0 {
		return RunnerConfig{}, fmt.Errorf("scan runner config: chunk_files must not be negative")
	}
	return cfg, nil
}

func bundledGatesConfig() (GatesConfig, error) {
	data, err := config.Read(config.ScannerGates)
	if err != nil {
		return GatesConfig{}, fmt.Errorf("read scan gates config: %w", err)
	}
	return decodeGatesConfig(data)
}

func decodeGatesConfig(data []byte) (GatesConfig, error) {
	var cfg GatesConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return GatesConfig{}, fmt.Errorf("parse scan gates config: %w", err)
	}
	if len(cfg.Gates.ProactiveCategories) == 0 {
		return GatesConfig{}, fmt.Errorf("scan gates config: proactive_categories required")
	}
	if cfg.Gates.AgentBudget.MaxHintsPerInjection <= 0 {
		return GatesConfig{}, fmt.Errorf("scan gates config: agent_budget.max_hints_per_injection must be positive")
	}
	if err := validateLandedChangeConfig(&cfg.Gates.LandedChange); err != nil {
		return GatesConfig{}, err
	}
	if err := normalizeCadenceConfig(&cfg.Gates.Cadence); err != nil {
		return GatesConfig{}, err
	}
	return cfg, nil
}

func normalizeCadenceConfig(config *CadenceConfig) error {
	if config == nil {
		return nil
	}
	if config.RefreshSettleMs == 0 {
		config.RefreshSettleMs = defaultRefreshSettleMs
	}
	if config.MaxDeferMs == 0 {
		config.MaxDeferMs = defaultMaxDeferMs
	}
	if config.WriteBurstSettleMs == 0 {
		config.WriteBurstSettleMs = defaultWriteBurstSettleMs
	}
	if config.ReconcileIntervalMs == 0 {
		config.ReconcileIntervalMs = defaultCadenceReconcileMs
	}
	if config.DriftMinPaths == 0 {
		config.DriftMinPaths = DefaultDriftMinPaths
	}
	if config.DriftRatio == 0 {
		config.DriftRatio = DefaultDriftRatio
	}
	if config.RefreshSettleMs < 0 {
		return fmt.Errorf("scan gates config: cadence.refresh_settle_ms must be non-negative")
	}
	if config.MaxDeferMs < config.RefreshSettleMs {
		return fmt.Errorf("scan gates config: cadence.max_defer_ms must be >= refresh_settle_ms")
	}
	if config.WriteBurstSettleMs < 0 {
		return fmt.Errorf("scan gates config: cadence.write_burst_settle_ms must be non-negative")
	}
	if config.ReconcileIntervalMs <= 0 {
		return fmt.Errorf("scan gates config: cadence.reconcile_interval_ms must be positive")
	}
	if config.DriftMinPaths < 0 {
		return fmt.Errorf("scan gates config: cadence.drift_min_paths must be non-negative")
	}
	if config.DriftRatio <= 0 || config.DriftRatio > 1 {
		return fmt.Errorf("scan gates config: cadence.drift_ratio must be in (0, 1]")
	}
	return nil
}

func (c CadenceConfig) RefreshSettle() time.Duration {
	ms := c.RefreshSettleMs
	if ms <= 0 {
		ms = defaultRefreshSettleMs
	}
	return time.Duration(ms) * time.Millisecond
}

func (c CadenceConfig) MaxDefer() time.Duration {
	ms := c.MaxDeferMs
	if ms <= 0 {
		ms = defaultMaxDeferMs
	}
	return time.Duration(ms) * time.Millisecond
}

func (c CadenceConfig) WriteBurstSettle() time.Duration {
	ms := c.WriteBurstSettleMs
	if ms <= 0 {
		ms = defaultWriteBurstSettleMs
	}
	return time.Duration(ms) * time.Millisecond
}

func (c CadenceConfig) ReconcileInterval() time.Duration {
	ms := c.ReconcileIntervalMs
	if ms <= 0 {
		ms = defaultCadenceReconcileMs
	}
	return time.Duration(ms) * time.Millisecond
}

func validateLandedChangeConfig(config *LandedChangeConfig) error {
	if config == nil {
		return nil
	}
	scope := LandedChangeScope(strings.TrimSpace(string(config.Scope)))
	if scope == "" {
		if config.Enabled {
			return fmt.Errorf("scan gates config: landed_change.scope required (path_scoped or full_root)")
		}
		config.Scope = LandedChangeScopePathScoped
		return nil
	}
	switch scope {
	case LandedChangeScopePathScoped, LandedChangeScopeFullRoot:
		config.Scope = scope
		return nil
	default:
		return fmt.Errorf("scan gates config: landed_change.scope must be path_scoped or full_root")
	}
}

func EffectiveLandedChangeScope(gates GatesConfig, sec *settings.SecurityScannersStore) LandedChangeScope {
	if sec != nil {
		if scope, ok := sec.OverlayLandedChangeScope(); ok {
			return LandedChangeScope(scope)
		}
	}
	scope := gates.Gates.LandedChange.Scope
	if scope == "" {
		return LandedChangeScopePathScoped
	}
	return scope
}

func normalizeRunnerConfig(cfg *RunnerConfig) {
	switch strings.ToLower(strings.TrimSpace(cfg.Runner.ProcessPriority)) {
	case "", string(exec.ProcessPriorityBelowNormal):
		cfg.Runner.ProcessPriority = string(exec.ProcessPriorityBelowNormal)
	case string(exec.ProcessPriorityNormal):
		cfg.Runner.ProcessPriority = string(exec.ProcessPriorityNormal)
	default:
		cfg.Runner.ProcessPriority = string(exec.ProcessPriorityBelowNormal)
	}
}

// ReconcileInterval bounds recovery after a missed notification.
func (c RunnerConfig) ReconcileInterval() time.Duration {
	return time.Duration(c.Runner.ReconcileIntervalMs) * time.Millisecond
}

// ExecProcessPriority maps runner config to exec.ProcessPriority.
func (c RunnerConfig) ExecProcessPriority() exec.ProcessPriority {
	if c.Runner.ProcessPriority == string(exec.ProcessPriorityNormal) {
		return exec.ProcessPriorityNormal
	}
	return exec.ProcessPriorityBelowNormal
}
