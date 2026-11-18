package rules

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/scan/opengrep"
)

type OpengrepGatesConfig struct {
	Analysis opengrep.Analysis `yaml:"analysis"`
	Vendor   []string          `yaml:"vendor"`
	Lycaon   []string          `yaml:"lycaon"`
}

func LoadOpengrepGates() (*OpengrepGatesConfig, error) {
	data, err := config.Read(config.OpengrepGates)
	if err != nil {
		return nil, err
	}
	var cfg OpengrepGatesConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse opengrep-gates: %w", err)
	}
	if err := cfg.Analysis.Validate(); err != nil {
		return nil, fmt.Errorf("opengrep-gates analysis: %w", err)
	}
	return &cfg, nil
}

// ActiveLycaonGatePaths returns configured first-party rule paths.
func ActiveLycaonGatePaths(cfg *OpengrepGatesConfig) []string {
	if cfg == nil {
		return nil
	}
	out := make([]string, 0, len(cfg.Lycaon))
	for _, p := range cfg.Lycaon {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ActiveVendorGatePaths returns configured vendor rule paths.
func ActiveVendorGatePaths(cfg *OpengrepGatesConfig) []string {
	if cfg == nil {
		return nil
	}
	out := make([]string, 0, len(cfg.Vendor))
	for _, p := range cfg.Vendor {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func isGateRuleYAML(path string) bool {
	if strings.HasSuffix(path, ".test.yaml") || strings.HasSuffix(path, ".test.yml") {
		return false
	}
	return strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")
}
