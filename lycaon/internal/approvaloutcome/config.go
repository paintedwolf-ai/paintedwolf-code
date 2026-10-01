// Package approvaloutcome renders approval resolution copy.
package approvaloutcome

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/config"
)

// Stable outcome codes identify catalog entries.
const (
	CodeApprovalDenied   = "approval_denied"
	CodeApprovalExpired  = "approval_expired"
	CodeApprovalCanceled = "approval_canceled"
)

// requiredCodes must carry rendered copy.
var requiredCodes = []string{CodeApprovalDenied, CodeApprovalExpired, CodeApprovalCanceled}

// Config is the on-disk approval-outcome-codes.yaml shape.
type Config struct {
	Outcomes map[string]Entry `yaml:"approval_outcomes"`
}

// Entry is one outcome row keyed by a stable code.
type Entry struct {
	Message   string          `yaml:"message"`
	Scenarios []ScenarioEntry `yaml:"scenarios"`
}

// ScenarioEntry is a contract-test fixture for rendered outcome copy.
type ScenarioEntry struct {
	ID             string         `yaml:"id"`
	Vars           map[string]any `yaml:"vars"`
	ExpectContains []string       `yaml:"expect_contains"`
}

// Load reads and validates the shipped outcome catalog.
func Load() (*Config, error) {
	data, err := config.Read(config.ApprovalOutcomeCodes)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return nil, err
	}
	if err := Validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks catalog structure and render scenarios.
func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("approval outcome config is nil")
	}
	for _, code := range requiredCodes {
		if _, ok := cfg.Outcomes[code]; !ok {
			return fmt.Errorf("approval_outcomes: required code %q missing", code)
		}
	}
	codes := make([]string, 0, len(cfg.Outcomes))
	for code := range cfg.Outcomes {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		entry := cfg.Outcomes[code]
		if strings.TrimSpace(entry.Message) == "" {
			return fmt.Errorf("approval_outcomes.%q: message is required", code)
		}
		if err := validatePongoField(code, entry.Message); err != nil {
			return err
		}
		if len(entry.Scenarios) == 0 {
			return fmt.Errorf("approval_outcomes.%q: scenarios required (>=1)", code)
		}
		for _, sc := range entry.Scenarios {
			if strings.TrimSpace(sc.ID) == "" {
				return fmt.Errorf("approval_outcomes.%q: scenario id required", code)
			}
			if len(sc.ExpectContains) == 0 {
				return fmt.Errorf("approval_outcomes.%q: scenario %q expect_contains required (>=1)", code, sc.ID)
			}
			if _, err := renderTemplateChecked(entry.Message, sc.Vars); err != nil {
				return fmt.Errorf("approval_outcomes.%q: scenario %q pongo render: %w", code, sc.ID, err)
			}
		}
	}
	return nil
}
