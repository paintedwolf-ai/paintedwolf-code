package evidence

import (
	"sort"
	"strings"
)

// RangeObserved reports whether the observation covers every line of the
// inclusive range for path. Recorded intervals may abut, but a gap between them
// fails even when both endpoints were seen. A record with no line facts for the
// path supports no range.
func RangeObserved(rec Record, path string, start, end int) bool {
	if start <= 0 || end < start {
		return false
	}
	claim := effectiveClaimPath(rec, path)
	if claim == "" || !pathObservedInRecord(rec, claim) {
		return false
	}
	if lines, ok := rec.grepLines[claim]; ok {
		return linesCoverRange(lines, start, end)
	}
	if len(rec.LineRanges) > 0 {
		return LineRangesCover(rec.LineRanges, []LineRange{{Start: start, End: end}})
	}
	return linesCoverRange(lineContentIndex(rec, claim), start, end)
}

func linesCoverRange(lines map[int]string, start, end int) bool {
	if end-start >= len(lines) {
		return false
	}
	for line := start; line <= end; line++ {
		if _, ok := lines[line]; !ok {
			return false
		}
	}
	return true
}

// ExcerptVerifiesRange reports that program output shows excerpt on a line inside the
// range. The host compared the text itself, so its reader did not have to count.
func ExcerptVerifiesRange(rec Record, path, excerpt string, start, end int) bool {
	fact := rec.SourceDelivery
	if fact == nil || excerpt == "" || !ExcerptMeaningful(excerpt) || !RangeObserved(rec, path, start, end) {
		return false
	}
	for line := max(start, fact.StartLine); line <= min(end, fact.EndLine); line++ {
		if index := line - fact.StartLine; index < len(rec.Body) && strings.Contains(rec.Body[index], excerpt) {
			return true
		}
	}
	return false
}

// UnionObserved reports whether the records together cover every line of the range
// for path. Each contributes the lines it showed numbered; a gap between them fails.
func UnionObserved(records []Record, path string, start, end int) bool {
	if start <= 0 || end < start {
		return false
	}
	var observed []LineRange
	for _, rec := range records {
		if rec.ShowedLineNumbers() {
			observed = append(observed, observedLines(rec, path)...)
		}
	}
	return LineRangesCover(observed, []LineRange{{Start: start, End: end}})
}

// observedLines lists the lines a record observed for path.
func observedLines(rec Record, path string) []LineRange {
	claim := effectiveClaimPath(rec, path)
	if claim == "" || !pathObservedInRecord(rec, claim) {
		return nil
	}
	lines, indexed := rec.grepLines[claim]
	switch {
	case indexed:
	case len(rec.LineRanges) > 0:
		return rec.LineRanges
	default:
		lines = lineContentIndex(rec, claim)
	}
	out := make([]LineRange, 0, len(lines))
	for line := range lines {
		out = append(out, LineRange{Start: line, End: line})
	}
	return out
}

// ExcerptAnchors returns, in order, the lines at which excerpt begins in the
// observation of path. A trivially short excerpt anchors nowhere.
func ExcerptAnchors(rec Record, path, excerpt string) []int {
	if len(excerpt) < EvidenceMinMeaningfulSpan {
		return nil
	}
	want := excerptLinesNormalized(excerpt)
	if len(want) == 0 {
		return nil
	}
	index := lineContentIndex(rec, path)
	var out []int
	for line := range index {
		if excerptLinesMatchAtAnchor(index, line, want) {
			out = append(out, line)
		}
	}
	sort.Ints(out)
	return out
}

// CoordinateFact is how a record stands behind a coordinate.
type CoordinateFact int

const (
	// CoordinateUnsupported means the record has no line facts for the location.
	CoordinateUnsupported CoordinateFact = iota
	// CoordinateObserved means the record observed every line.
	CoordinateObserved
	// CoordinateOnMutatedPath means the record changed the path without reporting
	// lines. A mutation supports any coordinate on its own path, traced to it.
	CoordinateOnMutatedPath
	// CoordinateDelivered means output delivered these lines without their numbers.
	// The reader counted, so the coordinate is traced and never exact.
	CoordinateDelivered
)

// CoordinateSupport decides a coordinate against one record. A mutation that reports
// its lines is held to them. A deletion leaves no line to cite.
func CoordinateSupport(rec Record, path string, start, end int) CoordinateFact {
	if !rec.ShowedLineNumbers() {
		if !rec.SourceDelivery.PrintedOtherNumbers && RangeObserved(rec, path, start, end) {
			return CoordinateDelivered
		}
		return CoordinateUnsupported
	}
	if RangeObserved(rec, path, start, end) {
		return CoordinateObserved
	}
	claim := effectiveClaimPath(rec, path)
	if start > 0 && end >= start && claim != "" && pathObservedInRecord(rec, claim) &&
		ActiveBinding().IsMutationKind(rec.Kind) && rec.Kind != "delete" && len(rec.LineRanges) == 0 && len(rec.Body) == 0 {
		return CoordinateOnMutatedPath
	}
	return CoordinateUnsupported
}

// PathsEndingIn lists the active ledger paths whose final segments are exactly token,
// such as `main.go` or `gateway/main.go`. A token with neither a dot nor a slash is
// prose: a bare word never names a path this way.
func PathsEndingIn(ev Ledger, token string) []string {
	token = NormalizeLedgerPath(token)
	if token == "" || strings.HasPrefix(token, "@") || !strings.ContainsAny(token, "./") || strings.Trim(token, "./") == "" {
		return nil
	}
	var out []string
	for path, handles := range ev.ByPath {
		if path != token && !strings.HasSuffix(path, "/"+token) {
			continue
		}
		for _, handle := range handles {
			if rec, ok := ResolveHandle(ev, handle); ok && rec.SupersededBy == "" && !rec.IsGate() {
				out = append(out, path)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}
