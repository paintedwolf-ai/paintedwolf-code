package webresearch

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/config"
)

// FetchURLLimits caps fetch frequency and concurrency.
type FetchURLLimits struct {
	SessionMaxFetches int
	SessionWindow     time.Duration
	HostMaxInFlight   int
	HostMinInterval   time.Duration
}

type fetchURLLimitsFile struct {
	Version  int `yaml:"version"`
	FetchURL struct {
		SessionMaxFetches int    `yaml:"session_max_fetches"`
		SessionWindow     string `yaml:"session_window"`
		HostMaxInFlight   int    `yaml:"host_max_in_flight"`
		HostMinInterval   string `yaml:"host_min_interval"`
	} `yaml:"fetch_url"`
}

var (
	bundledFetchLimitsOnce sync.Once
	bundledFetchLimits     FetchURLLimits
	bundledFetchLimitsErr  error
)

// DefaultFetchURLLimits loads the bundled fetch limits.
// Fail-closed: panics if the bundled file cannot be loaded.
func DefaultFetchURLLimits() FetchURLLimits {
	bundledFetchLimitsOnce.Do(func() {
		bundledFetchLimits, bundledFetchLimitsErr = LoadFetchURLLimits()
	})
	if bundledFetchLimitsErr != nil {
		panic(bundledFetchLimitsErr)
	}
	return bundledFetchLimits
}

// LoadFetchURLLimits reads the shipped web-research limits. Missing or invalid
// fails closed.
func LoadFetchURLLimits() (FetchURLLimits, error) {
	raw, err := config.Read(config.WebResearchLimits)
	if err != nil {
		return FetchURLLimits{}, fmt.Errorf("read web-research-limits: %w", err)
	}
	var file fetchURLLimitsFile
	if err := config.DecodeYAML(raw, &file); err != nil {
		return FetchURLLimits{}, fmt.Errorf("parse web-research-limits: %w", err)
	}
	if file.Version <= 0 {
		return FetchURLLimits{}, fmt.Errorf("web-research-limits: version must be positive")
	}
	out := FetchURLLimits{}
	if file.FetchURL.SessionMaxFetches > 0 {
		out.SessionMaxFetches = file.FetchURL.SessionMaxFetches
	}
	if file.FetchURL.HostMaxInFlight > 0 {
		out.HostMaxInFlight = file.FetchURL.HostMaxInFlight
	}
	if w := strings.TrimSpace(file.FetchURL.SessionWindow); w != "" {
		d, err := time.ParseDuration(w)
		if err != nil {
			return FetchURLLimits{}, fmt.Errorf("web-research-limits: session_window: %w", err)
		}
		if d <= 0 {
			return FetchURLLimits{}, fmt.Errorf("web-research-limits: session_window must be positive")
		}
		out.SessionWindow = d
	}
	if iv := strings.TrimSpace(file.FetchURL.HostMinInterval); iv != "" {
		d, err := time.ParseDuration(iv)
		if err != nil {
			return FetchURLLimits{}, fmt.Errorf("web-research-limits: host_min_interval: %w", err)
		}
		if d < 0 {
			return FetchURLLimits{}, fmt.Errorf("web-research-limits: host_min_interval must be >= 0")
		}
		out.HostMinInterval = d
	}
	if out.SessionMaxFetches <= 0 {
		return FetchURLLimits{}, fmt.Errorf("web-research-limits: session_max_fetches must be positive")
	}
	if out.SessionWindow <= 0 {
		return FetchURLLimits{}, fmt.Errorf("web-research-limits: session_window must be positive")
	}
	if out.HostMaxInFlight <= 0 {
		return FetchURLLimits{}, fmt.Errorf("web-research-limits: host_max_in_flight must be positive")
	}
	return out, nil
}
