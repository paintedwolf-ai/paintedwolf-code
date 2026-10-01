// Package userpath resolves one process-lifetime command PATH.
package userpath

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/config"
)

// MarkerPlaceholder marks framed probe output.
const MarkerPlaceholder = "{marker}"

// Config is the operator catalog behind PATH resolution.
type Config struct {
	Probe  ProbeConfig
	Shells []ShellInvocation
	// Fallback applies when probe and inherited entries are unusable.
	Fallback []string
}

// ProbeConfig bounds the login-shell probe.
type ProbeConfig struct {
	Timeout        time.Duration
	MaxOutputBytes int
	MaxEntries     int
	Marker         string
	EnvAllowlist   []string
}

// ShellInvocation defines a catalogued PATH probe.
type ShellInvocation struct {
	Names   []string
	Args    []string
	Command string
}

type configFile struct {
	Version int `yaml:"version"`
	Probe   struct {
		TimeoutMS      int      `yaml:"timeout_ms"`
		MaxOutputBytes int      `yaml:"max_output_bytes"`
		MaxEntries     int      `yaml:"max_entries"`
		Marker         string   `yaml:"marker"`
		EnvAllowlist   []string `yaml:"env_allowlist"`
	} `yaml:"probe"`
	Shells []struct {
		Names   []string `yaml:"names"`
		Args    []string `yaml:"args"`
		Command string   `yaml:"command"`
	} `yaml:"shells"`
	FallbackPath []string `yaml:"fallback_path"`
}

// ErrConfigInvalid reports a bundled catalog that cannot drive a probe.
var ErrConfigInvalid = errors.New("userpath: catalog invalid")

// LoadConfig reads the bundled catalog.
func LoadConfig() (Config, error) {
	raw, err := config.Read(config.UserPath)
	if err != nil {
		return Config{}, fmt.Errorf("userpath: read catalog: %w", err)
	}
	return parseConfig(raw)
}

func parseConfig(raw []byte) (Config, error) {
	var file configFile
	if err := config.DecodeYAML(raw, &file); err != nil {
		return Config{}, fmt.Errorf("userpath: parse catalog: %w", err)
	}
	cfg := Config{
		Probe: ProbeConfig{
			Timeout:        time.Duration(file.Probe.TimeoutMS) * time.Millisecond,
			MaxOutputBytes: file.Probe.MaxOutputBytes,
			MaxEntries:     file.Probe.MaxEntries,
			Marker:         strings.TrimSpace(file.Probe.Marker),
			EnvAllowlist:   trimmedNonEmpty(file.Probe.EnvAllowlist),
		},
		Fallback: trimmedNonEmpty(file.FallbackPath),
	}
	for _, shell := range file.Shells {
		cfg.Shells = append(cfg.Shells, ShellInvocation{
			Names:   trimmedNonEmpty(shell.Names),
			Args:    shell.Args,
			Command: strings.TrimSpace(shell.Command),
		})
	}
	return cfg, validateConfig(cfg)
}

func validateConfig(cfg Config) error {
	switch {
	case cfg.Probe.Timeout <= 0:
		return fmt.Errorf("%w: probe.timeout_ms must be positive", ErrConfigInvalid)
	case cfg.Probe.MaxOutputBytes <= 0:
		return fmt.Errorf("%w: probe.max_output_bytes must be positive", ErrConfigInvalid)
	case cfg.Probe.MaxEntries <= 0:
		return fmt.Errorf("%w: probe.max_entries must be positive", ErrConfigInvalid)
	case cfg.Probe.Marker == "":
		return fmt.Errorf("%w: probe.marker is required to frame shell output", ErrConfigInvalid)
	case len(cfg.Probe.EnvAllowlist) == 0:
		return fmt.Errorf("%w: probe.env_allowlist is required", ErrConfigInvalid)
	case len(cfg.Fallback) == 0:
		return fmt.Errorf("%w: fallback_path is required; an empty PATH is never an answer", ErrConfigInvalid)
	case len(cfg.Shells) == 0:
		return fmt.Errorf("%w: at least one shell invocation is required", ErrConfigInvalid)
	}
	for _, shell := range cfg.Shells {
		if len(shell.Names) == 0 {
			return fmt.Errorf("%w: shell entry has no names", ErrConfigInvalid)
		}
		if shell.Command == "" {
			return fmt.Errorf("%w: shell %q has no command", ErrConfigInvalid, shell.Names[0])
		}
		if !strings.Contains(shell.Command, MarkerPlaceholder) {
			return fmt.Errorf(
				"%w: shell %q command omits %s, so its output could not be framed",
				ErrConfigInvalid, shell.Names[0], MarkerPlaceholder,
			)
		}
	}
	for _, entry := range cfg.Fallback {
		if !isAcceptableEntry(entry) {
			return fmt.Errorf("%w: fallback_path entry %q is not an absolute path", ErrConfigInvalid, entry)
		}
	}
	return nil
}

// invocationFor returns the matching catalogued probe.
func (c Config) invocationFor(shellName string) (ShellInvocation, bool) {
	shellName = strings.TrimSpace(shellName)
	for _, shell := range c.Shells {
		for _, name := range shell.Names {
			if name == shellName {
				return shell, true
			}
		}
	}
	return ShellInvocation{}, false
}

func trimmedNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
