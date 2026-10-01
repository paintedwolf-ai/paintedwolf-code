package findings

import (
	"github.com/lycaon/lycaon/pkg/api"
)

// CountFindingsByLevel tallies normalized level counts.
func CountFindingsByLevel(findings []api.SecurityFinding) map[string]int {
	out := map[string]int{
		string(api.FindingLevelCritical): 0,
		string(api.FindingLevelHigh):     0,
		string(api.FindingLevelMedium):   0,
		string(api.FindingLevelLow):      0,
		string(api.FindingLevelInfo):     0,
	}
	for _, f := range findings {
		level := f.Level
		if level == "" {
			level = api.FindingLevelInfo
		}
		out[string(level)]++
	}
	return out
}

// BuildSarifStatistics returns structured post-budget scan statistics for evidence anchors.
func BuildSarifStatistics(findings []api.SecurityFinding) map[string]any {
	byLevel := map[string]int{}
	byKind := map[string]int{}
	rules := map[string]int{}
	for _, f := range findings {
		level := string(f.Level)
		if level == "" {
			level = string(api.FindingLevelInfo)
		}
		byLevel[level]++
		kind := string(FindingKind(f))
		byKind[kind]++
		if f.RuleID != "" {
			rules[f.RuleID]++
		}
	}
	return map[string]any{
		"total":    len(findings),
		"by_level": byLevel,
		"by_kind":  byKind,
		"rules":    rules,
	}
}
