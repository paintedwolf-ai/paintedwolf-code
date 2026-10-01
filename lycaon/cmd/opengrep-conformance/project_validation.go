package main

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

func validateProjectFamilies(projects []projectCase, bundle []byte) error {
	var selected struct {
		Rules []struct {
			ID       string `yaml:"id"`
			Metadata struct {
				Purpose string `yaml:"purpose"`
			} `yaml:"metadata"`
		} `yaml:"rules"`
	}
	if err := yaml.Unmarshal(bundle, &selected); err != nil {
		return fmt.Errorf("selected rule families: %w", err)
	}
	active := make(map[string]bool)
	for _, rule := range selected.Rules {
		active[rule.ID] = rule.Metadata.Purpose != "coverage"
	}
	for _, project := range projects {
		families := make(map[string]bool)
		for _, id := range project.Families {
			if !active[id] || families[id] {
				return fmt.Errorf("%s: duplicate or unselected security family %q", project.Name, id)
			}
			families[id] = true
		}
		for _, finding := range project.Findings {
			if !active[finding.Rule] || (len(families) > 0 && !families[finding.Rule]) {
				return fmt.Errorf("%s: expected finding uses an undeclared security family %q", project.Name, finding.Rule)
			}
		}
	}
	return nil
}
