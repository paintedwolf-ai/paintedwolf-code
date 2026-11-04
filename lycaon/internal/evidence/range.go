package evidence

import "sort"

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
)

// CoordinateSupport decides a coordinate against one record. A mutation that reports
// its lines is held to them.
func CoordinateSupport(rec Record, path string, start, end int) CoordinateFact {
	if RangeObserved(rec, path, start, end) {
		return CoordinateObserved
	}
	claim := effectiveClaimPath(rec, path)
	if start > 0 && end >= start && claim != "" && pathObservedInRecord(rec, claim) &&
		ActiveBinding().IsMutationKind(rec.Kind) && len(rec.LineRanges) == 0 && len(rec.Body) == 0 {
		return CoordinateOnMutatedPath
	}
	return CoordinateUnsupported
}
