package output

import (
	"encoding/json"
	"fmt"
	"strings"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
)

// FindingsJSONFormatVersion is the only accepted lycaon_findings_json format.
const FindingsJSONFormatVersion = 1

type findingsJSONParser struct{}

func newFindingsJSONParser() OutputParser { return findingsJSONParser{} }

func (findingsJSONParser) ID() string { return OutputParserFindingsJSON }

func (findingsJSONParser) Parse(raw []byte) (*Result, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("lycaon_findings_json: empty output")
	}
	var payload findingsJSONPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("lycaon_findings_json: invalid JSON (%d bytes): %w", len(raw), err)
	}
	if payload.Format == nil {
		return nil, fmt.Errorf("lycaon_findings_json: format is required")
	}
	if *payload.Format != FindingsJSONFormatVersion {
		return nil, fmt.Errorf("lycaon_findings_json: unsupported format %d (want %d)", *payload.Format, FindingsJSONFormatVersion)
	}
	if payload.Findings == nil {
		return nil, fmt.Errorf("lycaon_findings_json: findings array is required")
	}
	result := &Result{
		Categories: []api.ScanCategory{api.ScanCategorySecurity},
		Findings:   make([]api.SecurityFinding, 0, len(*payload.Findings)),
	}
	for _, row := range *payload.Findings {
		ruleID := strings.TrimSpace(row.RuleID)
		if ruleID == "" {
			// Skip rows with no rule id; keep the rest of the report.
			continue
		}
		locs := make([]api.SecurityFindingLocation, 0, len(row.Locations))
		for _, loc := range row.Locations {
			startLine := loc.StartLine
			if startLine < 0 {
				startLine = 0
			}
			locs = append(locs, api.SecurityFindingLocation{
				URI:       strings.TrimSpace(loc.URI),
				StartLine: startLine,
			})
		}
		result.Findings = append(result.Findings, scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
			DriverID:   OutputParserFindingsJSON,
			RuleID:     ruleID,
			Level:      normalizeFindingsJSONLevel(row.Level),
			Message:    row.Message,
			Kind:       api.FindingKindCustom,
			Locations:  locs,
			Categories: result.Categories,
		}))
	}
	result.FindingsCount = len(result.Findings)
	return result, nil
}

// normalizeFindingsJSONLevel returns the canonical level or unknown.
func normalizeFindingsJSONLevel(raw string) api.FindingLevel {
	if level, ok := findingsJSONLevel(raw); ok {
		return level
	}
	return api.FindingLevelUnknown
}

// findingsJSONLevel accepts canonical finding levels.
func findingsJSONLevel(raw string) (api.FindingLevel, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "critical":
		return api.FindingLevelCritical, true
	case "high":
		return api.FindingLevelHigh, true
	case "medium":
		return api.FindingLevelMedium, true
	case "low":
		return api.FindingLevelLow, true
	case "info":
		return api.FindingLevelInfo, true
	case "unknown":
		return api.FindingLevelUnknown, true
	default:
		return "", false
	}
}

type findingsJSONPayload struct {
	Format   *int               `json:"format"`
	Findings *[]findingsJSONRow `json:"findings"`
}

type findingsJSONRow struct {
	RuleID    string            `json:"rule_id"`
	Level     string            `json:"level"`
	Message   string            `json:"message"`
	Locations []findingsJSONLoc `json:"locations"`
}

type findingsJSONLoc struct {
	URI       string `json:"uri"`
	StartLine int    `json:"start_line"`
}
