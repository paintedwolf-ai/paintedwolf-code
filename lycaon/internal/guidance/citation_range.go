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
func applyCitationRange(finding WorkerFindingInput, ev evidence.Ledger, res evidence.Resolution) evidence.Resolution {
	res.EndLine = finding.EndLine
	if !res.Verdict.Grounded() {
		res.Line = finding.Line
		return res
	}
	refused := res
	refused.Line, refused.LineCorrectedFrom, refused.Verdict = finding.Line, 0, evidence.VerdictUnverifiable
	excerpt := strings.TrimSpace(finding.Excerpt)
	// A record that observed the lines outranks one that only changed the path.
	var fallback *evidence.Resolution
	for _, handle := range rangeCandidates(finding, ev, res) {
		rec, ok := evidence.ResolveHandle(ev, handle)
		if !ok || rec.IsGate() {
			continue
		}
		shift, ok := rangeExcerptShift(rec, res, finding, excerpt, handle)
		if !ok {
			return refused
		}
		start, end := finding.Line+shift, finding.EndLine+shift
		support := evidence.CoordinateSupport(rec, res.Path, start, end)
		if support == evidence.CoordinateUnsupported && citesFullDeliveryCoverage(finding, handle, rec, res.Path) {
			support = evidence.CoordinateDelivered
		}
		if support != evidence.CoordinateObserved && evidence.ExcerptVerifiesRange(rec, res.Path, excerpt, start, end) {
			support = evidence.CoordinateObserved
		}
		// Delivered lines carry a counted coordinate, never a quote they do not hold.
		if support == evidence.CoordinateUnsupported || (support == evidence.CoordinateDelivered && excerpt != "") {
			continue
		}
		bound := res
		bound.Handle, bound.Line, bound.EndLine, bound.LineCorrectedFrom = handle, start, end, 0
		if shift != 0 {
			bound.LineCorrectedFrom = finding.Line
		}
		if support == evidence.CoordinateObserved {
			return bound
		}
		if fallback == nil {
			bound.Verdict = evidence.VerdictTraced
			fallback = &bound
		}
	}
	if fallback != nil {
		return *fallback
	}
	if handle, ok := rangeAcrossRecords(finding, ev, res, excerpt); ok {
		bound := res
		bound.Handle, bound.Line, bound.EndLine, bound.LineCorrectedFrom = handle, finding.Line, finding.EndLine, 0
		return bound
	}
	return refused
}

// rangeAcrossRecords validates a path citation against the union of the path's
// active records, as when each line of a numbered read is its own record. It
// returns the record holding the range's first line. An explicit handle keeps to
// its own coverage, and an excerpt must anchor inside the range.
func rangeAcrossRecords(finding WorkerFindingInput, ev evidence.Ledger, res evidence.Resolution, excerpt string) (string, bool) {
	if _, known := evidence.ResolveHandle(ev, finding.Evidence); known {
		return "", false
	}
	handles := ev.ByPath[filepath.ToSlash(res.Path)]
	records := make([]evidence.Record, 0, len(handles))
	first, anchored := "", excerpt == ""
	for i := len(handles) - 1; i >= 0; i-- {
		rec, ok := evidence.ResolveHandle(ev, handles[i])
		if !ok || rec.SupersededBy != "" || rec.IsGate() {
			continue
		}
		records = append(records, rec)
		if first == "" && evidence.CoordinateSupport(rec, res.Path, finding.Line, finding.Line) == evidence.CoordinateObserved {
			first = handles[i]
		}
		for _, anchor := range evidence.ExcerptAnchors(rec, res.Path, excerpt) {
			anchored = anchored || (anchor >= finding.Line && anchor <= finding.EndLine)
		}
	}
	return first, first != "" && anchored && evidence.UnionObserved(records, res.Path, finding.Line, finding.EndLine)
}

// countedCoordinate requires a single-line citation without an excerpt to be
// within the selected source delivery. Output that omitted line numbers only
// traces coordinates the reader could count from the delivery.
func countedCoordinate(finding WorkerFindingInput, ev evidence.Ledger, res evidence.Resolution) evidence.Resolution {
	if finding.Line <= 0 || finding.EndLine > 0 || strings.TrimSpace(finding.Excerpt) != "" || !res.Verdict.Grounded() {
		return res
	}
	rec, ok := evidence.ResolveHandle(ev, res.Handle)
	// A write receipt grounds authorship separately from a source observation.
	if !ok || evidence.ActiveBinding().IsMutationKind(rec.Kind) ||
		(rec.SourceDelivery == nil && evidence.RecordShape(rec) != evidence.ShapeFileRegion) {
		return res
	}
	support := evidence.CoordinateSupport(rec, res.Path, finding.Line, finding.Line)
	if support == evidence.CoordinateObserved {
		return res
	}
	if _, known := evidence.ResolveHandle(ev, finding.Evidence); !known {
		for _, handle := range ev.ByPath[filepath.ToSlash(res.Path)] {
			if other, ok := evidence.ResolveHandle(ev, handle); ok && other.SupersededBy == "" && !other.IsGate() &&
				evidence.CoordinateSupport(other, res.Path, finding.Line, finding.Line) == evidence.CoordinateObserved {
				res.Handle = handle
				return res
			}
		}
	}
	res.Verdict = evidence.VerdictUnverifiable
	if support == evidence.CoordinateDelivered ||
		citesFullDeliveryCoverage(finding, res.Handle, rec, res.Path) {
		res.Verdict = evidence.VerdictTraced
	}
	return res
}

// citesFullDeliveryCoverage recognizes the host tag's whole observation when
// the output printed numbers from a different coordinate system, as cat -n
// does for later files. A subset still needs its own line evidence.
func citesFullDeliveryCoverage(finding WorkerFindingInput, handle string, rec evidence.Record, path string) bool {
	fact := rec.SourceDelivery
	if fact == nil || !fact.PrintedOtherNumbers || strings.TrimSpace(finding.Evidence) != handle ||
		strings.TrimSpace(finding.Path) == "" || strings.TrimSpace(finding.Excerpt) != "" {
		return false
	}
	end := finding.EndLine
	if end == 0 {
		end = finding.Line
	}
	return finding.Line == fact.StartLine && end == fact.EndLine && evidence.RangeObserved(rec, path, finding.Line, end)
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
	token := fmt.Sprintf("%s:%d", subject, line)
	if endLine > line {
		token = fmt.Sprintf("%s-%d", token, endLine)
	}
	if excerpt = strings.TrimSpace(excerpt); excerpt != "" {
		token += fmt.Sprintf(" (%q)", excerpt)
	}
	return token
}
