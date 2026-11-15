package grounding

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/config"
)

// GroundingConfig controls session and finding grounding behavior.
type GroundingConfig struct {
	PostTurn       GroundingModeConfig    `yaml:"post_turn"`
	Closeout       GroundingModeConfig    `yaml:"closeout"`
	Write          GroundingModeConfig    `yaml:"write"`
	Ambient        AmbientGroundingConfig `yaml:"ambient"`
	CircuitBreaker CircuitBreakerConfig   `yaml:"circuit_breaker"`
}

// AmbientGroundingConfig controls default Build worker-queue grounding.
type AmbientGroundingConfig struct {
	PostTurn GroundingModeConfig `yaml:"post_turn"`
}

// GroundingModeConfig is warn, block, or off.
type GroundingModeConfig struct {
	Mode string `yaml:"mode"`
}

// CircuitBreakerConfig limits repeated ungrounded claims.
type CircuitBreakerConfig struct {
	MaxUngroundedWarnings    int    `yaml:"max_ungrounded_warnings"`
	MaxConsecutiveUngrounded int    `yaml:"max_consecutive_ungrounded"`
	EscalateMode             string `yaml:"escalate_mode"`
}

var (
	bundledGroundingOnce sync.Once
	bundledGrounding     GroundingConfig
	bundledGroundingErr  error
)

// DefaultGroundingConfig loads the bundled grounding configuration.
func DefaultGroundingConfig() GroundingConfig {
	bundledGroundingOnce.Do(func() {
		bundledGrounding, bundledGroundingErr = LoadGroundingConfig()
	})
	if bundledGroundingErr != nil {
		panic(bundledGroundingErr)
	}
	return bundledGrounding
}

// LoadGroundingConfig reads grounding.yaml and fails closed on missing fields.
func LoadGroundingConfig() (GroundingConfig, error) {
	data, err := config.Read(config.Grounding)
	if err != nil {
		return GroundingConfig{}, fmt.Errorf("read grounding config: %w", err)
	}
	var cfg GroundingConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return GroundingConfig{}, fmt.Errorf("parse grounding config: %w", err)
	}
	if cfg.PostTurn.Mode == "" {
		return GroundingConfig{}, fmt.Errorf("grounding config: post_turn.mode required")
	}
	if cfg.Ambient.PostTurn.Mode == "" {
		cfg.Ambient.PostTurn.Mode = cfg.PostTurn.Mode
	}
	if cfg.Closeout.Mode == "" {
		return GroundingConfig{}, fmt.Errorf("grounding config: closeout.mode required")
	}
	if cfg.Write.Mode == "" {
		cfg.Write.Mode = cfg.PostTurn.Mode
	}
	if cfg.CircuitBreaker.MaxUngroundedWarnings <= 0 {
		return GroundingConfig{}, fmt.Errorf("grounding config: circuit_breaker.max_ungrounded_warnings must be positive")
	}
	if cfg.CircuitBreaker.MaxConsecutiveUngrounded <= 0 {
		return GroundingConfig{}, fmt.Errorf("grounding config: circuit_breaker.max_consecutive_ungrounded must be positive")
	}
	if cfg.CircuitBreaker.EscalateMode == "" {
		return GroundingConfig{}, fmt.Errorf("grounding config: circuit_breaker.escalate_mode required")
	}
	return cfg, nil
}

// WriteGroundingMode returns write.mode when set, otherwise post_turn.mode.
func (c GroundingConfig) WriteGroundingMode() string {
	if mode := strings.TrimSpace(c.Write.Mode); mode != "" {
		return mode
	}
	return strings.TrimSpace(c.PostTurn.Mode)
}

// AmbientPostTurnMode returns ambient post_turn.mode, defaulting to post_turn.mode.
func (c GroundingConfig) AmbientPostTurnMode() string {
	if mode := strings.TrimSpace(c.Ambient.PostTurn.Mode); mode != "" {
		return mode
	}
	return strings.TrimSpace(c.PostTurn.Mode)
}

// GroundingVerdict correlates coordinator output with ledger evidence.
type GroundingVerdict struct {
	OK     bool
	Code   string
	Reason string
	LegID  string
}

// WorkerSummaryTag identifies a parent worker_summary message without prose.
type WorkerSummaryTag struct {
	DelegationID string
	LegID        string
	JobID        string
	MessageID    string
	TS           time.Time
}

// ApplyCircuitBreaker updates counters after a grounding verdict.
func ApplyCircuitBreaker(state *UngroundedCounter, verdict GroundingVerdict, cfg GroundingConfig) {
	if verdict.OK {
		ResetUngroundedStreak(state)
		return
	}
	ApplyUngroundedWarning(
		state,
		cfg.CircuitBreaker.MaxUngroundedWarnings,
		cfg.CircuitBreaker.MaxConsecutiveUngrounded,
		cfg.CircuitBreaker.EscalateMode == "block",
	)
}

// Flagged reports whether accumulated warnings have crossed the circuit-breaker threshold.
func Flagged(state UngroundedCounter, cfg GroundingConfig) bool {
	cb := cfg.CircuitBreaker
	if cb.MaxConsecutiveUngrounded > 0 && state.ConsecutiveWarnings >= cb.MaxConsecutiveUngrounded {
		return true
	}
	return cb.MaxUngroundedWarnings > 0 && state.TotalWarnings >= cb.MaxUngroundedWarnings
}
