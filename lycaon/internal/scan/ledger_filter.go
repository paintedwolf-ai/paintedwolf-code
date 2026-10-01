package scan

import (
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

type ledgerQueryFilter struct {
	Levels          []string `json:"levels"`
	States          []string `json:"states"`
	Scanners        []string `json:"scanners"`
	Kind            string   `json:"kind"`
	Rule            string   `json:"rule"`
	Code            string   `json:"code"`
	Fingerprint     string   `json:"fingerprint"`
	Path            string   `json:"path"`
	PathPrefix      string   `json:"path_prefix"`
	Advisory        string   `json:"advisory"`
	Text            string   `json:"text"`
	IntroducedSince string   `json:"introduced_since"`
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func likeContains(value string) string {
	return "%" + escapeLike(value) + "%"
}

func newLedgerFilter(canonicalPath string, req api.FindingLedgerQueryRequest) (db.CountFindingLedgerParams, error) {
	filter := ledgerQueryFilter{
		Levels: normalizeLedgerLevels(req.Levels), States: normalizeLedgerStates(req.States),
		Scanners: normalizeStrings(req.ScannerIDs), Kind: strings.TrimSpace(req.Kind),
		Rule: strings.TrimSpace(req.RuleID), Code: strings.TrimSpace(req.Code),
		Fingerprint: strings.TrimSpace(req.Fingerprint),
		Path:        strings.Trim(strings.TrimSpace(req.Path), "/"),
	}
	if filter.Path != "" {
		filter.PathPrefix = escapeLike(filter.Path) + "/%"
	}
	if advisory := strings.ToLower(strings.TrimSpace(req.AdvisoryID)); advisory != "" {
		filter.Advisory = "% " + escapeLike(advisory) + " %"
	}
	if text := strings.TrimSpace(req.Text); text != "" {
		filter.Text = likeContains(strings.ToLower(text))
	}
	if req.IntroducedSinceAt != nil {
		filter.IntroducedSince = db.FormatTime(req.IntroducedSinceAt.UTC())
	}
	raw, err := surveyjson.Marshal(filter)
	if err != nil {
		return db.CountFindingLedgerParams{}, err
	}
	return db.CountFindingLedgerParams{CanonicalPath: canonicalPath, FilterJson: string(raw)}, nil
}

func normalizeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// normalizeLedgerLevels drops values outside the level vocabulary.
func normalizeLedgerLevels(levels []api.FindingLevel) []string {
	valid := make(map[api.FindingLevel]bool, len(api.AllFindingLevelValues()))
	for _, level := range api.AllFindingLevelValues() {
		valid[level] = true
	}
	out := make([]string, 0, len(levels))
	seen := make(map[api.FindingLevel]bool, len(levels))
	for _, level := range levels {
		if !valid[level] || seen[level] {
			continue
		}
		seen[level] = true
		out = append(out, string(level))
	}
	return out
}

func normalizeLedgerStates(states []api.FindingLedgerState) []string {
	valid := make(map[api.FindingLedgerState]bool)
	for _, state := range api.AllFindingLedgerStateValues() {
		valid[state] = true
	}
	out := make([]string, 0, len(states))
	seen := make(map[api.FindingLedgerState]bool, len(states))
	for _, state := range states {
		if !valid[state] || seen[state] {
			continue
		}
		seen[state] = true
		out = append(out, string(state))
	}
	return out
}
