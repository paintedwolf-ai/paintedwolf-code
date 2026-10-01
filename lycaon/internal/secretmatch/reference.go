package secretmatch

import (
	"context"
	"sort"
)

// Replacement is one written marker and the input range it covered. The input
// range lets a later pass move earlier markers; it is never recorded.
type Replacement struct {
	RedactionSpan
	// Reference is set when the marker is a capability reference.
	Reference              string
	SourceStart, SourceEnd int
}

// ProjectLabeledWhere rewrites a field for a model or transcript copy: live
// managed values become references, and matches accepted by keep become
// placeholders. A nil keep accepts every match.
func (m *Matcher) ProjectLabeledWhere(
	ctx context.Context,
	label, value string,
	keep func(Match) bool,
) (string, []Replacement) {
	hits := m.screenLabeledRaw(ctx, label, value)
	selected := hits[:0]
	for _, hit := range hits {
		if referenceable(hit) || keep == nil || keep(hit) {
			selected = append(selected, hit)
		}
	}
	return rewriteMatches(value, selected, true)
}

// referenceable reports exact managed evidence for a whole live value. Other
// hits may carry a propagated reference, but not the value's bounds.
func referenceable(hit Match) bool {
	return IsManagedRule(hit.RuleID) && hit.Reference != ""
}

// carveReferences splits iv into references for the managed values inside it
// and placeholders for the bytes a broader match claims around them.
func carveReferences(iv redactionInterval, hits []Match) []redactionInterval {
	var managed []Match
	for _, hit := range hits {
		if referenceable(hit) && hit.Start >= iv.start && hit.End <= iv.end && hit.End > hit.Start {
			managed = append(managed, hit)
		}
	}
	if len(managed) == 0 {
		return []redactionInterval{iv}
	}
	sort.SliceStable(managed, func(i, j int) bool {
		li, lj := managed[i].End-managed[i].Start, managed[j].End-managed[j].Start
		if li != lj {
			return li > lj
		}
		if managed[i].Start != managed[j].Start {
			return managed[i].Start < managed[j].Start
		}
		return managed[i].Reference < managed[j].Reference
	})
	chosen := make([]Match, 0, len(managed))
	for _, hit := range managed {
		if !overlapsAny(chosen, hit) {
			chosen = append(chosen, hit)
		}
	}
	sort.SliceStable(chosen, func(i, j int) bool { return chosen[i].Start < chosen[j].Start })

	out := make([]redactionInterval, 0, 2*len(chosen)+1)
	cursor := iv.start
	for _, hit := range chosen {
		if hit.Start > cursor {
			out = append(out, remainder(iv, hits, cursor, hit.Start))
		}
		out = append(out, redactionInterval{start: hit.Start, end: hit.End, by: hit, reference: hit.Reference})
		cursor = hit.End
	}
	if cursor < iv.end {
		out = append(out, remainder(iv, hits, cursor, iv.end))
	}
	return out
}

// remainder is a placeholder range named by the preferred match covering it.
func remainder(iv redactionInterval, hits []Match, start, end int) redactionInterval {
	gap := Match{Start: start, End: end}
	by, named := iv.by, false
	for _, hit := range hits {
		if !overlaps(hit, gap) {
			continue
		}
		if !named || prefer(hit, by) {
			by, named = hit, true
		}
	}
	return redactionInterval{start: start, end: end, by: by}
}

func overlapsAny(chosen []Match, hit Match) bool {
	for _, other := range chosen {
		if overlaps(other, hit) {
			return true
		}
	}
	return false
}
