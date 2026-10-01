package scan

import (
	"encoding/json"
	"slices"
	"strings"

	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/pkg/api"
)

// WarningsFromResult extracts scan warnings persisted in result_json.
func WarningsFromResult(raw json.RawMessage) []api.ScanWarning {
	if len(raw) == 0 {
		return nil
	}
	var result scanoutput.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}
	return result.Warnings
}

// SummarizeWarnings groups diagnostics without losing their separate audit rows.
func SummarizeWarnings(warnings []api.ScanWarning) []api.ScanWarningSummary {
	type group struct {
		count int
		files map[string]bool
		rules map[string]bool
	}
	groups := map[api.ScanWarningKind]*group{}
	seen := map[api.ScanWarning]bool{}
	for _, warning := range warnings {
		if seen[warning] {
			continue
		}
		seen[warning] = true
		g := groups[warning.Kind]
		if g == nil {
			g = &group{files: map[string]bool{}, rules: map[string]bool{}}
			groups[warning.Kind] = g
		}
		g.count++
		if warning.File != "" {
			g.files[warning.File] = true
		}
		if warning.RuleID != "" {
			g.rules[warning.RuleID] = true
		}
	}
	var out []api.ScanWarningSummary
	for kind, g := range groups {
		out = append(out, api.ScanWarningSummary{Kind: kind, Count: g.count, Files: len(g.files), Rules: len(g.rules)})
	}
	slices.SortFunc(out, func(a, b api.ScanWarningSummary) int { return strings.Compare(string(a.Kind), string(b.Kind)) })
	return out
}
