package workflowadmin

import (
	"strings"

	"github.com/lycaon/lycaon/internal/report"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// citation is one place or handle something in the report rests on, and
// who rests on it.
type citation struct {
	by     string
	handle string
	path   string
	line   int
}

// closeoutCitations are the report's own citations, attributed to the report.
func closeoutCitations(g *wire.CitationGrounding) []citation {
	if g == nil {
		return nil
	}
	out := make([]citation, 0, len(g.CitedEvidence))
	for _, c := range g.CitedEvidence {
		out = append(out, citation{
			by:     report.CitedByReport,
			handle: strings.TrimSpace(c.Handle),
			path:   strings.TrimSpace(c.Path),
			line:   c.Line,
		})
	}
	return out
}

// verdictCitations are every claim's citations, attributed to the claim, and
// each verdict's own citation channel, attributed to its phase.
func verdictCitations(verdicts []report.ReportVerdict, channels map[string][]citation) []citation {
	var out []citation
	for _, v := range verdicts {
		for _, c := range v.Claims {
			for _, cite := range c.CitedEvidence {
				out = append(out, citation{by: c.ID, handle: cite.Handle, path: cite.Path, line: cite.Line})
			}
		}
		out = append(out, channels[v.Phase]...)
	}
	return out
}

// evidenceFromGrounding projects the closeout's ledger sample onto appendix
// rows and marks each with everything that cites it: the report, a claim,
// or a review phase. A citation resolves by handle, by the exact place it
// names, or by a line inside the span a read observed.
func evidenceFromGrounding(g *wire.CitationGrounding, cites []citation) ([]report.ReportEvidence, int) {
	if g == nil {
		return nil, 0
	}
	out := make([]report.ReportEvidence, 0, len(g.EvidenceRecords))
	for _, rec := range g.EvidenceRecords {
		scope, handle := splitHandleScope(rec.Handle)
		row := report.ReportEvidence{
			Handle:     handle,
			Scope:      scope,
			Kind:       rec.Kind,
			Path:       rec.Path,
			Line:       rec.Line,
			LineEnd:    rec.LineEnd,
			TrustTier:  rec.Fidelity,
			Matches:    rec.MatchCount,
			MatchFiles: rec.MatchPaths,
			Truncated:  rec.Truncated,
		}
		// An excerpt is a quotation from a place. A record that names no place
		// has nothing to quote from, and its captured body is a tool payload.
		if strings.TrimSpace(rec.Path) != "" {
			row.Excerpt = rec.Excerpt
		}
		if len(rec.URLs) > 0 {
			row.URL = strings.TrimSpace(rec.URLs[0])
		}
		row.CitedBy = citedBy(rec, cites)
		out = append(out, row)
	}
	if len(out) == 0 {
		return nil, 0
	}
	total := g.EvidenceRecordCount
	if total < len(out) {
		total = len(out)
	}
	return out, total
}

// splitHandleScope divides a ledger handle into the leg it was recorded in and
// the handle itself, so the appendix can drop a session id that identifies
// nothing and costs its narrowest column the width.
func splitHandleScope(handle string) (string, string) {
	handle = strings.TrimSpace(handle)
	cut := strings.LastIndex(handle, ":")
	if cut < 0 {
		return "", handle
	}
	return handle[:cut], handle[cut+1:]
}

// citedBy lists, in first-seen order, everything whose citations resolve to
// this record.
func citedBy(rec wire.CitationGroundingEvidenceRecord, cites []citation) []string {
	handle := strings.TrimSpace(rec.Handle)
	path := strings.TrimSpace(rec.Path)
	var out []string
	seen := map[string]struct{}{}
	for _, c := range cites {
		if c.by == "" || !citationMatches(c, handle, path, rec.Line, rec.LineEnd) {
			continue
		}
		if _, ok := seen[c.by]; ok {
			continue
		}
		seen[c.by] = struct{}{}
		out = append(out, c.by)
	}
	return out
}

func citationMatches(c citation, handle, path string, line, lineEnd int) bool {
	if c.handle != "" && handle != "" && c.handle == handle {
		return true
	}
	if c.path == "" || path == "" || c.path != path {
		return false
	}
	switch {
	case c.line == 0:
		return true
	case c.line == line:
		return true
	case lineEnd > 0 && c.line >= line && c.line <= lineEnd:
		return true
	default:
		return false
	}
}

// sourcesFromGrounding lists the pages the report and its verdicts cited,
// titled where a tool saw the page's title. The report's own citations come
// first in the order it gave them; each verdict's follow in phase order.
func sourcesFromGrounding(g *wire.CitationGrounding, verdicts []report.ReportVerdict, verdictURLs map[string][]string) []report.ReportSource {
	titles := map[string]string{}
	if g != nil {
		for _, rec := range g.EvidenceRecords {
			for u, t := range rec.URLTitles {
				if u, t = strings.TrimSpace(u), strings.TrimSpace(t); u != "" && t != "" {
					titles[u] = t
				}
			}
		}
	}
	var out []report.ReportSource
	at := map[string]int{}
	add := func(raw, by string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		i, ok := at[raw]
		if !ok {
			i = len(out)
			at[raw] = i
			out = append(out, report.ReportSource{URL: raw, Title: titles[raw]})
		}
		for _, existing := range out[i].CitedBy {
			if existing == by {
				return
			}
		}
		out[i].CitedBy = append(out[i].CitedBy, by)
	}
	if g != nil {
		for _, u := range g.CitedURLs {
			add(u, report.CitedByReport)
		}
	}
	for _, v := range verdicts {
		for _, u := range verdictURLs[v.Phase] {
			add(u, v.Phase)
		}
	}
	return out
}
