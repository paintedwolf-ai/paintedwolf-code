package scan

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/advisory"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/paginate"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
)

const maxQueryPageSize = 1000

var scanQueryPages = pagecursor.For[int]("scan_query")

func queryScope(req QueryRequest) string {
	introStr, fixedStr := "", ""
	if req.IntroducedSince != nil {
		introStr = req.IntroducedSince.UTC().Format(time.RFC3339Nano)
	}
	if req.FixedSince != nil {
		fixedStr = req.FixedSince.UTC().Format(time.RFC3339Nano)
	}
	return pagecursor.Scope(
		req.ScanID, req.Level, req.Kind, req.AdvisoryID,
		req.Fingerprint, req.RuleID, req.Path, req.Code,
		fmt.Sprint(req.Dedupe), introStr, fixedStr,
	)
}

// QueryRequest filters stored scan findings for drill-down.
type QueryRequest struct {
	ScanID      string
	Level       string
	Kind        string
	AdvisoryID  string
	Fingerprint string
	RuleID      string
	Path        string
	Code        string
	Cursor      string
	Offset      int
	Limit       int
	Dedupe      bool
	// IntroducedSince keeps only findings the series first observed at or
	// after this time; FixedSince also returns what it fixed since then.
	IntroducedSince *time.Time
	FixedSince      *time.Time
}

// Query filters persisted findings and their attached guidance for a completed scan.
func (c *CoordinatorImpl) Query(ctx context.Context, req QueryRequest) (*api.ScanQueryResponse, error) {
	if c == nil || c.Store == nil {
		return nil, fmt.Errorf("scan coordinator not configured")
	}
	scanID := strings.TrimSpace(req.ScanID)
	if scanID == "" {
		return nil, fmt.Errorf("scan_id required")
	}
	scan, err := c.Summary(ctx, scanID)
	if err != nil {
		return nil, err
	}
	if scan == nil {
		return nil, &DrilldownReject{
			Code: DrilldownRejectNotFound,
			Data: map[string]any{"scan_id": scanID},
		}
	}
	if scan.Status != api.CodeScanStatusComplete {
		return nil, &DrilldownReject{
			Code: DrilldownRejectNotComplete,
			Data: map[string]any{
				"scan_id": scanID,
				"status":  string(scan.Status),
			},
		}
	}
	if scan.FindingSetID != "" && scan.FindingsStored > 0 {
		var out *api.ScanQueryResponse
		if req.IntroducedSince != nil {
			// Introduction filters require history stored separately from the finding index.
			out, err = c.Store.queryIntroducedSince(ctx, scan, req)
		} else {
			out, err = c.Store.queryFindingEntries(ctx, scan, req)
		}
		if err != nil {
			return nil, err
		}
		return c.Store.attachFixedSince(ctx, scan, req, out)
	}
	filtered := filterGuidance(scan.Guidance, req)
	if req.Dedupe {
		filtered = dedupeQueryGuidance(filtered)
	}
	out, err := paginateQueryResponse(scanID, nil, filtered, req)
	if err != nil {
		return nil, err
	}
	return c.Store.attachFixedSince(ctx, scan, req, out)
}

func QuerySecurityFindings(scanID string, findings []api.SecurityFinding, guidance []api.ScanGuidanceSummary, req QueryRequest) (*api.ScanQueryResponse, error) {
	if req.ScanID == "" {
		req.ScanID = scanID
	}
	filtered := FilterFindings(findings, req)
	if req.Dedupe {
		filtered = dedupeQueryFindings(filtered)
	}
	matchedGuidance := guidanceForFindings(filtered, guidance)
	return paginateQueryResponse(scanID, filtered, matchedGuidance, req)
}

func paginateQueryResponse(scanID string, findings []api.SecurityFinding, guidance []api.ScanGuidanceSummary, req QueryRequest) (*api.ScanQueryResponse, error) {
	scope := queryScope(req)
	offset := 0
	if req.Cursor != "" {
		dec, err := scanQueryPages.Decode(req.Cursor, scope)
		if err != nil {
			return nil, err
		}
		offset = dec
	}
	offset = max(offset, 0)
	limit := req.Limit
	if limit <= 0 {
		limit = scancfg.DefaultAgentBudget().MaxQueryResults
	}
	limit = min(limit, maxQueryPageSize)
	var (
		page       []api.SecurityFinding
		guidPage   []api.ScanGuidanceSummary
		total      int
		truncated  bool
		nextOffset *int
	)
	if len(findings) > 0 {
		page, total, truncated, nextOffset = paginate.Slice(findings, offset, limit)
		guidPage = guidanceForFindings(page, guidance)
	} else {
		guidPage, total, truncated, nextOffset = paginate.Slice(guidance, offset, limit)
	}
	resp := &api.ScanQueryResponse{
		ScanID:     scanID,
		Findings:   page,
		Guidance:   nonNilGuidance(guidPage),
		TotalMatch: total,
		Truncated:  truncated,
	}
	if nextOffset != nil {
		token, err := scanQueryPages.Encode(scope, *nextOffset)
		if err != nil {
			return nil, err
		}
		resp.NextCursor = token
	}
	return resp, nil
}

func nonNilGuidance(in []api.ScanGuidanceSummary) []api.ScanGuidanceSummary {
	if in == nil {
		return []api.ScanGuidanceSummary{}
	}
	return in
}

func FilterFindings(in []api.SecurityFinding, req QueryRequest) []api.SecurityFinding {
	if len(in) == 0 {
		return nil
	}
	levelFilter := strings.TrimSpace(req.Level)
	out := make([]api.SecurityFinding, 0, len(in))
	for _, f := range in {
		if levelFilter != "" && !strings.EqualFold(string(f.Level), levelFilter) {
			continue
		}
		if req.Kind != "" && !strings.EqualFold(string(scanfindings.FindingKind(f)), req.Kind) {
			continue
		}
		if req.AdvisoryID != "" && !advisoryIDMatch(f, req.AdvisoryID) {
			continue
		}
		if req.Fingerprint != "" && !strings.EqualFold(strings.TrimSpace(f.Fingerprints.Primary), req.Fingerprint) {
			continue
		}
		if req.Code != "" && !strings.EqualFold(hintCode(f), req.Code) {
			continue
		}
		if req.RuleID != "" && !strings.Contains(f.RuleID, req.RuleID) {
			continue
		}
		if req.Path != "" {
			uri, _ := primaryLocation(f)
			if uri == "" || !strings.HasPrefix(uri, req.Path) {
				continue
			}
		}
		out = append(out, f)
	}
	return out
}

func advisoryIDMatch(f api.SecurityFinding, want string) bool {
	want = strings.TrimSpace(strings.ToLower(want))
	if want == "" {
		return true
	}
	for _, id := range advisory.IDs(scanfindings.Advisory(f)) {
		if strings.EqualFold(id, want) {
			return true
		}
	}
	return false
}

func dedupeQueryFindings(in []api.SecurityFinding) []api.SecurityFinding {
	seen := map[string]struct{}{}
	out := make([]api.SecurityFinding, 0, len(in))
	for _, f := range in {
		key := strings.TrimSpace(f.Fingerprints.Primary)
		if key == "" {
			uri, line := primaryLocation(f)
			key = f.RuleID + "\x00" + uri + "\x00" + fmt.Sprintf("%d", line)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, f)
	}
	return out
}

func guidanceForFindings(findings []api.SecurityFinding, guidance []api.ScanGuidanceSummary) []api.ScanGuidanceSummary {
	if len(findings) == 0 || len(guidance) == 0 {
		return nil
	}
	byRule := map[string][]api.ScanGuidanceSummary{}
	for _, g := range guidance {
		byRule[g.RuleID] = append(byRule[g.RuleID], g)
	}
	out := make([]api.ScanGuidanceSummary, 0, len(findings))
	for _, f := range findings {
		rows := byRule[f.RuleID]
		if len(rows) == 0 {
			continue
		}
		uri, line := primaryLocation(f)
		matched := rows[0]
		for _, g := range rows {
			if g.File == uri && (line == 0 || g.Line == line) {
				matched = g
				break
			}
		}
		out = append(out, matched)
	}
	return out
}

func filterGuidance(in []api.ScanGuidanceSummary, req QueryRequest) []api.ScanGuidanceSummary {
	if len(in) == 0 {
		return nil
	}
	levelFilter := strings.TrimSpace(req.Level)
	out := make([]api.ScanGuidanceSummary, 0, len(in))
	for _, g := range in {
		if req.Code != "" && !strings.EqualFold(g.Code, req.Code) {
			continue
		}
		if levelFilter != "" && !strings.EqualFold(string(scanfindings.NormalizeVendorSeverity(g.Severity)), levelFilter) {
			continue
		}
		if req.RuleID != "" && !strings.Contains(g.RuleID, req.RuleID) {
			continue
		}
		if req.Path != "" {
			if g.File == "" || !strings.HasPrefix(g.File, req.Path) {
				continue
			}
		}
		out = append(out, g)
	}
	return out
}

func dedupeQueryGuidance(in []api.ScanGuidanceSummary) []api.ScanGuidanceSummary {
	seen := map[string]struct{}{}
	out := make([]api.ScanGuidanceSummary, 0, len(in))
	for _, g := range in {
		key := g.Code + "\x00" + g.File
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, g)
	}
	return out
}

// queryIntroducedSince loads the set, stamps history, and keeps the findings
// the series first observed at or after the requested time.
func (s *SQLStore) queryIntroducedSince(ctx context.Context, scan *api.CodeScan, req QueryRequest) (*api.ScanQueryResponse, error) {
	findings, err := loadFindingEntries(ctx, s.queries, scan.FindingSetID)
	if err != nil {
		return nil, err
	}
	if err := s.attachFindingHistory(ctx, scan.CanonicalPath, scan.ScannerID, findings); err != nil {
		return nil, err
	}
	since := req.IntroducedSince.UTC()
	kept := make([]api.SecurityFinding, 0, len(findings))
	for _, finding := range findings {
		if finding.History != nil && !finding.History.IntroducedAt.Before(since) {
			kept = append(kept, finding)
		}
	}
	return QuerySecurityFindings(scan.ID, kept, scan.Guidance, req)
}

// attachFixedSince adds the findings the series fixed at or after the
// requested time, under the same filters as the page.
func (s *SQLStore) attachFixedSince(ctx context.Context, scan *api.CodeScan, req QueryRequest, out *api.ScanQueryResponse) (*api.ScanQueryResponse, error) {
	if req.FixedSince == nil || out == nil {
		return out, nil
	}
	fixed, err := s.FixedFindingsSince(ctx, scan.CanonicalPath, scan.ScannerID, *req.FixedSince)
	if err != nil {
		return nil, err
	}
	out.FixedFindings = FilterFindings(fixed, req)
	return out, nil
}
