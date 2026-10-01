package scan

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

type findingLocationCount struct {
	URI   string `json:"uri"`
	Line  int    `json:"line"`
	Rank  int    `json:"rank"`
	Count int    `json:"count"`
}

// findingRollup retains exact aggregate inputs without finding bodies.
type findingRollup struct {
	WarningSummary []api.ScanWarningSummary   `json:"warning_summary,omitempty"`
	Count          int                        `json:"count"`
	ByLevel        map[string]int             `json:"by_level"`
	ByKind         map[string]int             `json:"by_kind"`
	Unmapped       int                        `json:"unmapped"`
	Locations      []findingLocationCount     `json:"locations,omitempty"`
	TopLocations   []api.BoardScanTopLocation `json:"top_locations,omitempty"`
}

func rollupFindings(findings []api.SecurityFinding) findingRollup {
	r := findingRollup{Count: len(findings), ByLevel: scanfindings.CountFindingsByLevel(findings),
		ByKind: CountFindingsByKind(findings), Unmapped: CountUnmappedSAST(findings)}
	counts := map[findingLocationCount]int{}
	for _, f := range findings {
		uri, line := primaryLocation(f)
		if uri != "" {
			counts[findingLocationCount{URI: uri, Line: line, Rank: api.FindingLevelRank(f.Level)}]++
		}
	}
	for key, count := range counts {
		key.Count = count
		r.Locations = append(r.Locations, key)
	}
	return r
}

func refreshAssessmentRollups(ctx context.Context, q *db.Queries, scanID string) error {
	ids, err := q.ScanAssessmentIDs(ctx, scanID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := refreshAssessmentRollup(ctx, q, id); err != nil {
			return err
		}
	}
	return nil
}

func refreshAssessmentRollup(ctx context.Context, q *db.Queries, id string) error {
	if err := q.RefreshAssessmentComplete(ctx, id); err != nil {
		return err
	}
	members, err := q.AssessmentRollupMembers(ctx, id)
	if err != nil {
		return err
	}
	var signature strings.Builder
	for _, member := range members {
		signature.WriteString(member.ScanID + "\x00" + member.FindingSetID + "\x00")
	}
	previous, err := q.GetAssessmentRollup(ctx, id)
	if err == nil && previous.MemberSignature == signature.String() {
		return nil
	}
	if err != nil && !db.IsNoRows(err) {
		return err
	}
	parts := make([]findingRollup, 0, len(members))
	for _, member := range members {
		if member.FindingSetID == "" {
			continue
		}
		raw, err := q.GetFindingRollup(ctx, member.FindingSetID)
		if err != nil {
			return err
		}
		var part findingRollup
		if err := json.Unmarshal([]byte(raw), &part); err != nil {
			return err
		}
		parts = append(parts, part)
	}
	merged := mergeFindingRollups(parts)
	merged.WarningSummary, err = assessmentWarningSummary(ctx, q, id)
	if err != nil {
		return err
	}
	raw, err := surveyjson.Marshal(merged)
	if err != nil {
		return err
	}
	return q.PutAssessmentRollup(ctx, db.PutAssessmentRollupParams{
		AssessmentID: id, MemberSignature: signature.String(), RollupJson: string(raw),
	})
}

func mergeFindingRollups(parts []findingRollup) findingRollup {
	out := findingRollup{ByLevel: map[string]int{}, ByKind: map[string]int{}}
	locations := map[findingLocationCount]int{}
	for _, part := range parts {
		out.Count += part.Count
		out.Unmapped += part.Unmapped
		for k, n := range part.ByLevel {
			out.ByLevel[k] += n
		}
		for k, n := range part.ByKind {
			out.ByKind[k] += n
		}
		for _, loc := range part.Locations {
			n := loc.Count
			loc.Count = 0
			locations[loc] += n
		}
	}
	ordered := make([]findingLocationCount, 0, len(locations))
	for loc, n := range locations {
		loc.Count = n
		ordered = append(ordered, loc)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Rank != b.Rank {
			return a.Rank < b.Rank
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.URI != b.URI {
			return a.URI < b.URI
		}
		return a.Line < b.Line
	})
	for _, loc := range ordered[:min(len(ordered), boardTopLocationsStoreCap)] {
		out.TopLocations = append(out.TopLocations, api.BoardScanTopLocation{URI: loc.URI, StartLine: loc.Line})
	}
	return out
}

func (s *SQLStore) assessmentSummary(ctx context.Context, id string, scans []api.CodeScan) (*api.BoardScanSummary, error) {
	summary := BuildAssessmentSummary(id, scans)
	if summary == nil {
		return nil, nil
	}
	row, err := s.queries.GetAssessmentRollup(ctx, id)
	if err != nil {
		return nil, err
	}
	var r findingRollup
	if err := json.Unmarshal([]byte(row.RollupJson), &r); err != nil {
		return nil, err
	}
	summary.FindingsCount = r.Count
	summary.FindingsByLevel = r.ByLevel
	summary.FindingsByKind = r.ByKind
	summary.UnmappedCount = r.Unmapped
	summary.TopLocations = r.TopLocations
	summary.WarningSummary = r.WarningSummary
	return summary, nil
}

func assessmentWarningSummary(ctx context.Context, q *db.Queries, id string) ([]api.ScanWarningSummary, error) {
	rows, err := q.AssessmentWarnings(ctx, id)
	if err != nil {
		return nil, err
	}
	var warnings []api.ScanWarning
	for _, raw := range rows {
		var part []api.ScanWarning
		if err := json.Unmarshal([]byte(raw), &part); err != nil {
			return nil, err
		}
		warnings = append(warnings, part...)
	}
	return SummarizeWarnings(warnings), nil
}
