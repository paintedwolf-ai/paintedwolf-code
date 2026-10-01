package rules

import (
	"fmt"
	"path/filepath"

	"github.com/lycaon/lycaon/config"
)

// RulesConfig is the on-disk rules YAML shape.
type RulesConfig struct {
	Rules []RuleEntry `yaml:"rules"`
}

// RuleEntry is one posture rule: a when condition map and its then outcome.
type RuleEntry struct {
	ID   string         `yaml:"id"`
	When map[string]any `yaml:"when"`
	Then map[string]any `yaml:"then"`
}

// LoadRulesConfig reads one shipped posture-rules file.
func LoadRulesConfig(rel config.Rel) (*RulesConfig, error) {
	data, err := config.Read(rel)
	if err != nil {
		return nil, err
	}
	var cfg RulesConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadBundledRules loads every shipped posture-rules file, keyed by base name.
func LoadBundledRules() (map[string]*RulesConfig, error) {
	entries, err := config.List(config.PostureRulesDir)
	if err != nil {
		return nil, err
	}
	packs := make(map[string]*RulesConfig)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		cfg, err := LoadRulesConfig(config.PostureRulesDir.Join(e.Name()))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		key := NormalizeRulesPath(config.PostureRulesDir.Join(e.Name()).String())
		packs[key] = cfg
	}
	if len(packs) == 0 {
		return nil, fmt.Errorf("no rule files in %s", config.PostureRulesDir)
	}
	if err := ValidateRulesIndex(packs); err != nil {
		return nil, err
	}
	return packs, nil
}

// LoadBundledRuleConfigs loads every shipped posture-rules file.
func LoadBundledRuleConfigs() ([]*RulesConfig, error) {
	packs, err := LoadBundledRules()
	if err != nil {
		return nil, err
	}
	configs := make([]*RulesConfig, 0, len(packs))
	for _, cfg := range packs {
		configs = append(configs, cfg)
	}
	return configs, nil
}
