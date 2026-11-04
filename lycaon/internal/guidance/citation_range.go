package guidance

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
)

// applyCitationRange checks a grounded ranged citation against what its record
// actually observed. It adds a check to the single-line result and never reduces
// a range to its first line.
func applyCitationRange(roots evidence.CitationRoots, finding WorkerFindingInput, ev evidence.Ledger, res evidence.Resolution) evidence.Resolution {
	res.EndLine = finding.EndLine
	if !res.Verdict.Grounded() {
		res.Line = finding.Line
		return res
	}
	refused := res
	refused.Line, refused.LineCorrectedFrom, refused.Verdict = finding.Line, 0, evidence.VerdictUnverifiable
	excerpt := strings.TrimSpace(finding.Excerpt)
	for _, handle := range rangeCandidates(finding, ev, res) {
		rec, ok := evidence.ResolveHandle(ev, handle)
		if !ok {
			continue
		}
		shift, ok := rangeExcerptShift(rec, res, finding, excerpt, handle)
		if !ok {
			return refused
		}
		start, end := finding.Line+shift, finding.EndLine+shift
		if !evidence.RangeObserved(rec, res.Path, start, end) {
			continue
		}
		res.Handle, res.Line, res.EndLine, res.LineCorrectedFrom = handle, start, end, 0
		if shift != 0 {
			res.LineCorrectedFrom = finding.Line
		}
		return res
	}
	return refused
}

// rangeCandidates lists the records that could support the range. An explicit
// handle is the only candidate; a path citation may land on any active record
// of that path, newest first.
func rangeCandidates(finding WorkerFindingInput, ev evidence.Ledger, res evidence.Resolution) []string {
	if _, known := evidence.ResolveHandle(ev, finding.Evidence); known {
		return []string{strings.TrimSpace(finding.Evidence)}
	}
	var out []string
	if res.Handle != "" {
		out = append(out, res.Handle)
	}
	if res.Verdict == evidence.VerdictBound {
		return out
	}
	handles := ev.ByPath[filepath.ToSlash(res.Path)]
	for i := len(handles) - 1; i >= 0; i-- {
		if rec, ok := evidence.ResolveHandle(ev, handles[i]); ok && rec.SupersededBy == "" && !rec.IsGate() && handles[i] != res.Handle {
			out = append(out, handles[i])
		}
	}
	return out
}

// rangeExcerptShift finds how far an excerpt moves the range. An excerpt inside
// the range moves nothing. One just outside, within the bind window, moves both
// ends; two such places are ambiguous and ok is false. Farther away, only the
// resolver's own unique re-anchor can move it.
func rangeExcerptShift(rec evidence.Record, res evidence.Resolution, finding WorkerFindingInput, excerpt, handle string) (shift int, ok bool) {
	var near []int
	for _, anchor := range evidence.ExcerptAnchors(rec, res.Path, excerpt) {
		switch {
		case anchor >= finding.Line && anchor <= finding.EndLine:
			return 0, true
		case anchor < finding.Line && finding.Line-anchor <= evidence.EvidenceLineBindWindow:
			near = append(near, anchor-finding.Line)
		case anchor > finding.EndLine && anchor-finding.EndLine <= evidence.EvidenceLineBindWindow:
			near = append(near, anchor-finding.EndLine)
		}
	}
	switch {
	case len(near) == 1:
		return near[0], true
	case len(near) > 1:
		return 0, false
	case res.LineCorrectedFrom > 0 && res.Handle == handle:
		return res.Line - res.LineCorrectedFrom, true
	}
	return 0, true
}

// formatRangeOffender renders a ranged citation for reject and audit tokens.
func formatRangeOffender(path, handle string, line, endLine int, excerpt string) string {
	subject := path
	if subject == "" {
		subject = strings.TrimSpace(handle)
	}
	token := fmt.Sprintf("%s:%d-%d", subject, line, endLine)
	if excerpt = strings.TrimSpace(excerpt); excerpt != "" {
		token += fmt.Sprintf(" (%q)", excerpt)
	}
	return token
}
