package scan

import (
	"sort"
	"strings"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
)

const boardTopLocationsStoreCap = 5

// ShortHeadSHA returns the first 8 hex chars of a git object id when long enough.
func ShortHeadSHA(headSHA string) string {
	head := strings.TrimSpace(headSHA)
	if len(head) >= 8 {
		return head[:8]
	}
	return head
}

// BoardRollup carries pack-board scan summary fields derived from stored ingest.
type BoardRollup struct {
	HeadShort       string
	FindingsByLevel map[string]int
	TopLocations    []api.BoardScanTopLocation
	FindingsByKind  map[string]int
	UnmappedSAST    int
}

// BuildBoardRollup derives board scan presentation fields from a code scan row.
func BuildBoardRollup(scan *api.CodeScan) BoardRollup {
	if scan == nil {
		return BoardRollup{}
	}
	byLevel := scan.FindingsByLevel
	if byLevel == nil && len(scan.Findings) > 0 {
		byLevel = scanfindings.CountFindingsByLevel(scan.Findings)
	}
	rollup := BoardRollup{
		HeadShort: ShortHeadSHA(scan.HeadSHA), FindingsByLevel: byLevel,
		TopLocations: scan.TopLocations, FindingsByKind: scan.FindingsByKind, UnmappedSAST: scan.UnmappedCount,
	}
	if len(scan.Findings) > 0 {
		rollup.TopLocations = TopLocationsFromFindings(scan.Findings, boardTopLocationsStoreCap)
		rollup.FindingsByKind = CountFindingsByKind(scan.Findings)
		rollup.UnmappedSAST = CountUnmappedSAST(scan.Findings)
	}
	return rollup
}

// ApplyBoardRollup copies rollup fields onto BoardScanSummary.
func ApplyBoardRollup(summary *api.BoardScanSummary, scan *api.CodeScan) {
	if summary == nil || scan == nil {
		return
	}
	rollup := BuildBoardRollup(scan)
	if rollup.HeadShort != "" {
		summary.HeadShort = rollup.HeadShort
	}
	if len(rollup.FindingsByLevel) > 0 {
		summary.FindingsByLevel = rollup.FindingsByLevel
	}
	if len(rollup.FindingsByKind) > 0 {
		summary.FindingsByKind = rollup.FindingsByKind
	}
	if rollup.UnmappedSAST > 0 {
		summary.UnmappedCount = rollup.UnmappedSAST
	}
	if len(rollup.TopLocations) > 0 {
		summary.TopLocations = rollup.TopLocations
	}
}

// CountFindingsByKind tallies findings by properties.lycaon.kind.
func CountFindingsByKind(findings []api.SecurityFinding) map[string]int {
	if len(findings) == 0 {
		return nil
	}
	out := map[string]int{}
	for _, f := range findings {
		kind := string(scanfindings.FindingKind(f))
		if kind == "" {
			kind = string(api.FindingKindCustom)
		}
		out[kind]++
	}
	return out
}

// CountUnmappedSAST counts SAST rows still mapped to SCAN_FINDING_UNMAPPED.
func CountUnmappedSAST(findings []api.SecurityFinding) int {
	n := 0
	for _, f := range findings {
		if scanfindings.FindingKind(f) != api.FindingKindSAST {
			continue
		}
		if hintCode(f) == "SCAN_FINDING_UNMAPPED" {
			n++
		}
	}
	return n
}

func hintCode(f api.SecurityFinding) string {
	if f.Properties == nil || f.Properties.Lycaon == nil {
		return ""
	}
	return strings.TrimSpace(f.Properties.Lycaon.HintCode)
}

// TopLocationsFromFindings returns uri:line hotspots ordered by severity then count.
func TopLocationsFromFindings(findings []api.SecurityFinding, limit int) []api.BoardScanTopLocation {
	if limit <= 0 || len(findings) == 0 {
		return nil
	}
	type locKey struct {
		uri  string
		line int
		rank int
	}
	counts := map[locKey]int{}
	for _, f := range findings {
		uri, line := primaryLocation(f)
		if uri == "" {
			continue
		}
		k := locKey{uri: uri, line: line, rank: api.FindingLevelRank(f.Level)}
		counts[k]++
	}
	if len(counts) == 0 {
		return nil
	}
	keys := make([]locKey, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].rank != keys[j].rank {
			return keys[i].rank < keys[j].rank
		}
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		if keys[i].uri != keys[j].uri {
			return keys[i].uri < keys[j].uri
		}
		return keys[i].line < keys[j].line
	})
	if len(keys) > limit {
		keys = keys[:limit]
	}
	out := make([]api.BoardScanTopLocation, 0, len(keys))
	for _, k := range keys {
		out = append(out, api.BoardScanTopLocation{URI: k.uri, StartLine: k.line})
	}
	return out
}

func primaryLocation(f api.SecurityFinding) (uri string, line int) {
	if len(f.Locations) == 0 {
		return "", 0
	}
	loc := f.Locations[0]
	return strings.TrimSpace(loc.URI), loc.StartLine
}

const boardCompareTopNewCap = 2

// BuildBoardCompareSlice aggregates compatible per-scanner comparisons between
// two complete assessments.
func BuildBoardCompareSlice(baselineAssessmentID string, responses ...*Comparison) *api.BoardScanCompareSlice {
	if len(responses) == 0 {
		return nil
	}
	out := &api.BoardScanCompareSlice{
		BaselineAssessmentID: strings.TrimSpace(baselineAssessmentID),
		NewByLevel:           map[string]int{},
	}
	var newFindings []api.SecurityFinding
	for _, resp := range responses {
		if resp == nil {
			continue
		}
		out.ScannersCompared++
		out.NewCount += resp.NewCount
		out.ResolvedCount += resp.ResolvedCount
		for level, count := range resp.NewByLevel {
			out.NewByLevel[level] += count
		}
		newFindings = append(newFindings, resp.NewFindings...)
	}
	if out.ScannersCompared == 0 {
		return nil
	}
	if len(newFindings) > 0 {
		out.TopNew = BoardCompareStubsFromFindings(newFindings, boardCompareTopNewCap)
	}
	return out
}

// BoardCompareStubsFromFindings returns capped file:rule stubs for full board detail.
func BoardCompareStubsFromFindings(findings []api.SecurityFinding, limit int) []api.BoardScanCompareStub {
	if limit <= 0 || len(findings) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]api.BoardScanCompareStub, 0, limit)
	for _, f := range findings {
		file, _ := primaryLocation(f)
		rule := strings.TrimSpace(f.RuleID)
		if file == "" || rule == "" {
			continue
		}
		key := file + "\x00" + rule
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, api.BoardScanCompareStub{File: file, RuleID: rule})
		if len(out) >= limit {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
