package main

import (
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/scan/rules"
)

func loadProjectRules(candidate, root string) ([]byte, error) {
	if candidate != "" {
		raw, err := os.ReadFile(candidate) // #nosec G304 -- explicitly selected developer rule file.
		if err != nil {
			return nil, fmt.Errorf("candidate rules: %w", err)
		}
		return rules.CompileGateRuleFiles([]rules.GateRuleFile{{Name: candidate, Data: raw}})
	}
	config, err := rules.LoadOpengrepGates()
	if err != nil {
		return nil, err
	}
	return rules.CompileGateRules(config, root)
}
