package ignores

import (
	"strings"
	"time"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
)

// IgnoredFinding is a run's audit record of one finding an entry covered.
type IgnoredFinding struct {
	Fingerprint string `json:"fingerprint,omitempty"`
	RuleID      string `json:"rule_id,omitempty"`
	File        string `json:"file,omitempty"`
	EntryID     string `json:"entry_id,omitempty"`
	MatchedOn   string `json:"matched_on,omitempty"`
	Reason      string `json:"reason"`
	Expires     string `json:"expires,omitempty"`
}

// PartitionIgnored retains ignored findings for subsequent scan comparisons.
func PartitionIgnored(
	catalog *IgnoreCatalog, findings []api.SecurityFinding, now time.Time,
) (active []api.SecurityFinding, ignored []IgnoredFinding) {
	if catalog == nil || len(catalog.Rules) == 0 {
		return findings, nil
	}
	active = make([]api.SecurityFinding, 0, len(findings))
	for _, finding := range findings {
		rule, ok := catalog.Match(IgnoreSubjectFor(finding), now)
		if !ok {
			active = append(active, finding)
			continue
		}
		ignored = append(ignored, IgnoredFinding{
			Fingerprint: finding.Fingerprints.Primary,
			RuleID:      finding.RuleID,
			File:        scanfindings.PrimaryURI(finding),
			EntryID:     rule.ID,
			MatchedOn:   rule.MatchedOn(),
			Reason:      rule.Reason,
			Expires:     rule.Expires,
		})
	}
	return active, ignored
}

// MatchedOn renders the entry's predicates in the ignore file's syntax.
func (e IgnoreEntry) MatchedOn() string {
	parts := make([]string, 0, 6)
	add := func(key, value string) {
		if value = strings.TrimSpace(value); value != "" {
			parts = append(parts, key+": "+value)
		}
	}
	add("path", e.Path)
	add("kind", e.Kind)
	add("scanner", e.Scanner)
	add("rule", e.Rule)
	add("advisory", e.Advisory)
	add("fingerprint", e.Fingerprint)
	return strings.Join(parts, " · ")
}

func ToAPIIgnored(in []IgnoredFinding) []api.ScanIgnoredFinding {
	if len(in) == 0 {
		return nil
	}
	out := make([]api.ScanIgnoredFinding, 0, len(in))
	for _, row := range in {
		out = append(out, api.ScanIgnoredFinding{
			Fingerprint: row.Fingerprint,
			RuleID:      row.RuleID,
			File:        row.File,
			EntryID:     row.EntryID,
			MatchedOn:   row.MatchedOn,
			Reason:      row.Reason,
			ExpiresOn:   row.Expires,
		})
	}
	return out
}
