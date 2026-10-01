package rules

import (
	"slices"

	"gopkg.in/yaml.v3"
)

// GenericRuleIDs identifies rules whose text matcher needs source syntax context.
func GenericRuleIDs(bundle []byte) (map[string]bool, error) {
	var file struct {
		Rules []struct {
			ID        string   `yaml:"id"`
			Languages []string `yaml:"languages"`
			Metadata  struct {
				Purpose string `yaml:"purpose"`
			} `yaml:"metadata"`
		} `yaml:"rules"`
	}
	if err := yaml.Unmarshal(bundle, &file); err != nil {
		return nil, err
	}
	ids := make(map[string]bool)
	for _, rule := range file.Rules {
		if slices.Contains(rule.Languages, "generic") && rule.Metadata.Purpose != "coverage" {
			ids[rule.ID] = true
		}
	}
	return ids, nil
}

// CoverageRuleIDs separates catalog coverage observations from vulnerability findings.
func CoverageRuleIDs(bundle []byte) (map[string]bool, error) {
	var file struct {
		Rules []struct {
			ID       string `yaml:"id"`
			Metadata struct {
				Purpose string `yaml:"purpose"`
			} `yaml:"metadata"`
		} `yaml:"rules"`
	}
	if err := yaml.Unmarshal(bundle, &file); err != nil {
		return nil, err
	}
	ids := make(map[string]bool)
	for _, rule := range file.Rules {
		if rule.Metadata.Purpose == "coverage" {
			ids[rule.ID] = true
		}
	}
	return ids, nil
}
