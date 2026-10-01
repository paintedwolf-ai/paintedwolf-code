package guidance

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

// bindLooseCitation binds an unverifiable citation to a unique matching ledger record.
func bindLooseCitation(projectDir string, finding WorkerFindingInput, ev evidence.Ledger, res evidence.Resolution) evidence.Resolution {
	candidates := ledgerRecordsMatching(projectDir, finding, ev)
	switch len(candidates) {
	case 0:
		return res
	case 1:
		res.Handle = candidates[0]
		if strings.TrimSpace(res.Path) == "" {
			if rec, ok := evidence.ResolveHandle(ev, candidates[0]); ok {
				res.Path = rec.Path
			}
		}
		res.Verdict = evidence.VerdictBound
		return res
	default:
		res.Verdict = evidence.VerdictAmbiguous
		return res
	}
}

// ledgerRecordsMatching returns handles of ledger records satisfying all supplied finding fields.
func ledgerRecordsMatching(projectDir string, finding WorkerFindingInput, ev evidence.Ledger) []string {
	line := finding.Line
	excerpt := strings.TrimSpace(finding.Excerpt)
	// A trivially short excerpt never grounds, so it never binds either.
	if excerpt != "" && !evidence.ExcerptMeaningful(excerpt) {
		return nil
	}
	normPath := citationPathFilter(projectDir, finding.Path, &line)
	if normPath == "" && excerpt == "" {
		return nil
	}
	var out []string
	for _, handle := range evidence.HandlesSorted(ev) {
		rec, ok := evidence.ResolveHandle(ev, handle)
		if !ok || rec.IsGate() || rec.SupersededBy != "" {
			continue
		}
		if normPath != "" && filepath.ToSlash(strings.TrimSpace(rec.Path)) != normPath {
			continue
		}
		if line > 0 && len(rec.LineRanges) > 0 && !evidence.LineInRanges(line, rec.LineRanges) {
			continue
		}
		if excerpt != "" && !evidence.ExcerptMatchesHandle(ev, handle, 0, excerpt) {
			continue
		}
		out = append(out, handle)
	}
	return out
}

// citationPathFilter keeps handle recovery unconstrained by a path.
func citationPathFilter(projectDir, raw string, line *int) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if evidence.HandleGrammar.MatchString(raw) {
		return ""
	}
	if p, ln, ok := evidence.SplitPathLineToken(raw); ok {
		raw = p
		if line != nil && *line <= 0 {
			*line = ln
		}
	}
	return normalizedReportPath(evidence.CitationRoots{ProjectDir: projectDir}, raw, nil)
}

// BindAdvisoryTokens formats the audit advisory for every host-bound citation in a
// resolution set: "bound <cited> → <handle>". A bind is advisory, not a reject — it
// never increments the friction counters.
func BindAdvisoryTokens(resolved []evidence.Resolution) []string {
	var out []string
	for _, res := range resolved {
		if res.Verdict != evidence.VerdictBound {
			continue
		}
		cited := FormatResolutionOffender(res, WorkerFindingInput{})
		handle := strings.TrimSpace(res.Handle)
		if handle == "" {
			out = append(out, fmt.Sprintf("bound %s", cited))
			continue
		}
		out = append(out, fmt.Sprintf("bound %s → %s", cited, handle))
	}
	return out
}

// WireCitationVerdict maps an internal resolver verdict to the citation wire enum.
// Host-bound citations are grounded, so they surface as matched on the wire (the
// bind advisory carries the provenance distinction); ambiguous never reaches the
// wire because it is filtered as a reject upstream.
func WireCitationVerdict(v evidence.Verdict) api.CitationVerdict {
	if v == evidence.VerdictBound {
		return api.CitationVerdictMatched
	}
	return api.CitationVerdict(v)
}
