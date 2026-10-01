package scan

import (
	"context"
	"encoding/json"
	"strings"

	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/pkg/api"
)

// queryFindingEntries selects ordinals before loading the page's finding bodies.
func (s *SQLStore) queryFindingEntries(ctx context.Context, scan *api.CodeScan, req QueryRequest) (*api.ScanQueryResponse, error) {
	if req.ScanID == "" && scan != nil {
		req.ScanID = scan.ID
	}
	where, args := findingEntryFilter(scan.FindingSetID, req)
	selection := "SELECT ordinal FROM scan_finding_entries WHERE " + where
	if req.Dedupe {
		selection = `SELECT MIN(ordinal) AS ordinal FROM scan_finding_entries WHERE ` + where + ` GROUP BY
            COALESCE(NULLIF(TRIM(json_extract(finding_json, '$.fingerprints.primary')), ''),
              json_extract(finding_json, '$.rule_id') || char(0) ||
              TRIM(COALESCE(json_extract(finding_json, '$.locations[0].uri'), '')) || char(0) ||
              COALESCE(json_extract(finding_json, '$.locations[0].start_line'), 0))`
	}
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ("+selection+")", args...).Scan(&total); err != nil {
		return nil, err
	}
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
	query := `SELECT finding_json FROM scan_finding_entries
        WHERE finding_set_id = ? AND ordinal IN (` + selection + ` ORDER BY ordinal LIMIT ? OFFSET ?) ORDER BY ordinal`
	pageArgs := append([]any{scan.FindingSetID}, args...)
	pageArgs = append(pageArgs, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, pageArgs...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	findings := make([]api.SecurityFinding, 0, min(limit, total))
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var finding api.SecurityFinding
		if err := json.Unmarshal([]byte(raw), &finding); err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	response := &api.ScanQueryResponse{
		ScanID:     scan.ID,
		Findings:   findings,
		Guidance:   nonNilGuidance(guidanceForFindings(findings, scan.Guidance)),
		TotalMatch: total,
	}
	if offset+len(findings) < total {
		token, err := scanQueryPages.Encode(scope, offset+len(findings))
		if err != nil {
			return nil, err
		}
		response.NextCursor = token
		response.Truncated = true
	}
	return response, nil
}

func findingEntryFilter(setID string, req QueryRequest) (string, []any) {
	clauses := []string{"finding_set_id = ?"}
	args := []any{setID}
	for _, filter := range []struct{ expression, value string }{
		{"json_extract(finding_json, '$.level')", strings.TrimSpace(req.Level)},
		{"COALESCE(NULLIF(json_extract(finding_json, '$.properties.lycaon.kind'), ''), 'custom')", req.Kind},
		{"TRIM(json_extract(finding_json, '$.fingerprints.primary'))", req.Fingerprint},
		{"TRIM(json_extract(finding_json, '$.properties.lycaon.hint_code'))", req.Code},
	} {
		if filter.value != "" {
			clauses = append(clauses, "LOWER("+filter.expression+") = ?")
			args = append(args, strings.ToLower(filter.value))
		}
	}
	if req.RuleID != "" {
		clauses = append(clauses, "instr(json_extract(finding_json, '$.rule_id'), ?) > 0")
		args = append(args, req.RuleID)
	}
	if req.Path != "" {
		clauses = append(clauses, "substr(TRIM(json_extract(finding_json, '$.locations[0].uri')), 1, length(?)) = ?")
		args = append(args, req.Path, req.Path)
	}
	if id := strings.TrimSpace(strings.ToLower(req.AdvisoryID)); id != "" {
		clauses = append(clauses, `(LOWER(TRIM(json_extract(finding_json, '$.properties.lycaon.advisory.osv_id'))) = ?
            OR EXISTS (SELECT 1 FROM json_each(finding_json, '$.properties.lycaon.advisory.cve_ids') WHERE LOWER(TRIM(value)) = ?)
            OR EXISTS (SELECT 1 FROM json_each(finding_json, '$.properties.lycaon.advisory.ghsa_ids') WHERE LOWER(TRIM(value)) = ?)
            OR EXISTS (SELECT 1 FROM json_each(finding_json, '$.properties.lycaon.advisory.aliases') WHERE LOWER(TRIM(value)) = ?))`)
		args = append(args, id, id, id, id)
	}
	return strings.Join(clauses, " AND "), args
}
