package recall

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/search"
)

func queryTargetsRecall(in search.SearchInterpretation) bool {
	for _, f := range in.Filters {
		if f.Negated {
			continue
		}
		field := strings.ToLower(strings.TrimSpace(f.Field))
		val := strings.ToLower(strings.TrimSpace(f.Value))
		switch field {
		case "tool", "kind":
			if val == ToolName {
				return true
			}
		case "handle":
			if strings.HasPrefix(val, ToolName+"#") {
				return true
			}
		case "id", "ref":
			return true
		}
	}
	for _, term := range in.FTSTerms {
		if strings.Contains(strings.ToLower(term), ToolName) {
			return true
		}
	}
	return false
}

func filterSelfReferentialRecallHits(hits []search.Hit) []search.Hit {
	out := make([]search.Hit, 0, len(hits))
	for _, h := range hits {
		if strings.EqualFold(strings.TrimSpace(h.Tool), ToolName) ||
			strings.EqualFold(strings.TrimSpace(h.Kind), ToolName) ||
			strings.HasPrefix(strings.ToLower(strings.TrimSpace(h.Handle)), ToolName+"#") {
			continue
		}
		out = append(out, h)
	}
	return out
}

// coalesceToolCallHits drops handleless assistant tool-call rows when the
// corresponding settled tool-result row is already in the hit set, carrying over
// any structured path or tool name from the call row.
func coalesceToolCallHits(hits []search.Hit) []search.Hit {
	type callMeta struct {
		path string
		tool string
	}
	calls := make(map[string]callMeta)
	hasResult := make(map[string]bool)
	for _, h := range hits {
		ref := strings.TrimSpace(h.SourceRef)
		session := strings.TrimSpace(h.SessionID)
		if ref == "" || session == "" || h.HitKind != search.HitKindTool {
			continue
		}
		key := session + "\x00" + ref
		switch h.Source {
		case search.SourceTool:
			hasResult[key] = true
		case search.SourceMessage:
			if strings.TrimSpace(h.Handle) == "" {
				calls[key] = callMeta{
					path: strings.TrimSpace(h.Path),
					tool: strings.TrimSpace(h.Tool),
				}
			}
		}
	}
	if len(hasResult) == 0 || len(calls) == 0 {
		return hits
	}
	out := make([]search.Hit, 0, len(hits))
	for _, h := range hits {
		ref := strings.TrimSpace(h.SourceRef)
		session := strings.TrimSpace(h.SessionID)
		key := session + "\x00" + ref
		if ref != "" && session != "" && h.HitKind == search.HitKindTool {
			if h.Source == search.SourceMessage && strings.TrimSpace(h.Handle) == "" && hasResult[key] {
				continue
			}
			if h.Source == search.SourceTool {
				if meta, ok := calls[key]; ok {
					if strings.TrimSpace(h.Path) == "" && meta.path != "" {
						h.Path = meta.path
					}
					if strings.TrimSpace(h.Tool) == "" && meta.tool != "" {
						h.Tool = meta.tool
					}
				}
			}
		}
		out = append(out, h)
	}
	return out
}

func distinctObservationCount(hits []search.Hit) int {
	seen := make(map[string]struct{}, len(hits))
	count := 0
	for _, h := range hits {
		handle := strings.TrimSpace(h.Handle)
		session := strings.TrimSpace(h.SessionID)
		if handle == "" || session == "" {
			count++
			continue
		}
		key := session + "\x00" + handle
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		count++
	}
	return count
}

type recordMeta struct {
	found          bool
	kind           string
	fidelity       string
	sourceTool     string
	path           string
	url            string
	startLine      int
	endLine        int
	marksUntrusted bool
}

func (s *Service) enrichFromEvidenceRecords(ctx context.Context, hits []Hit) {
	memo := make(map[string]recordMeta, len(hits))
	for i := range hits {
		handle := strings.TrimSpace(hits[i].Handle)
		session := strings.TrimSpace(hits[i].SessionID)
		if handle == "" || session == "" {
			continue
		}
		key := session + "\x00" + handle
		meta, ok := memo[key]
		if !ok {
			meta = s.loadRecordMeta(ctx, session, handle)
			memo[key] = meta
		}
		if !meta.found {
			continue
		}
		if hits[i].Path == "" && meta.path != "" {
			hits[i].Path = meta.path
		}
		if hits[i].Line <= 0 && meta.startLine > 0 {
			hits[i].Line = meta.startLine
		}
		if hits[i].EndLine <= 0 && meta.endLine > hits[i].Line && hits[i].Line > 0 {
			hits[i].EndLine = meta.endLine
		}
		if hits[i].Tool == "" && meta.sourceTool != "" {
			hits[i].Tool = meta.sourceTool
		}
		if (hits[i].Kind == "" || hits[i].Kind == search.HitKindEvidence || hits[i].Kind == search.HitKindTool) && meta.kind != "" {
			hits[i].Kind = meta.kind
		}
		if hits[i].Trust == "" && meta.fidelity != "" {
			hits[i].Trust = meta.fidelity
		}
		if hits[i].URL == "" && meta.url != "" {
			hits[i].URL = meta.url
		}
		if meta.marksUntrusted {
			hits[i].Untrusted = true
		}
	}
}

func (s *Service) loadRecordMeta(ctx context.Context, session, handle string) recordMeta {
	var kind, fidelity, sourceTool, path, url, lineRanges string
	var marksUntrusted int64
	err := s.db.QueryRowContext(ctx, `
		SELECT kind, fidelity, source_tool, path, url, line_ranges, marks_untrusted
		FROM evidence_records
		WHERE session_id = ? AND handle = ?
	`, session, handle).Scan(&kind, &fidelity, &sourceTool, &path, &url, &lineRanges, &marksUntrusted)
	if err != nil {
		return recordMeta{}
	}
	startLine, endLine := parseEvidenceRecordSpan(lineRanges)
	return recordMeta{
		found:          true,
		kind:           strings.TrimSpace(kind),
		fidelity:       strings.TrimSpace(fidelity),
		sourceTool:     strings.ToLower(strings.TrimSpace(sourceTool)),
		path:           cleanRecallPath(path),
		url:            strings.TrimSpace(url),
		startLine:      startLine,
		endLine:        endLine,
		marksUntrusted: marksUntrusted != 0,
	}
}

func parseEvidenceRecordSpan(raw string) (start, end int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, 0
	}
	var payload struct {
		Ranges []evidence.LineRange `json:"ranges"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil || len(payload.Ranges) == 0 {
		return 0, 0
	}
	start = payload.Ranges[0].Start
	end = payload.Ranges[len(payload.Ranges)-1].End
	return start, end
}

func cleanRecallPath(raw string) string {
	p := strings.TrimSpace(raw)
	for strings.HasPrefix(p, "./") {
		p = strings.TrimSpace(strings.TrimPrefix(p, "./"))
	}
	return p
}
