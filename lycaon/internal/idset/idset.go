// Package idset holds set operations over identifier slices. Results are sorted
// and deduplicated, so two of them compare as sets rather than by insertion order.
package idset

import (
	"sort"
	"strings"
)

// UnionSorted returns the deduplicated, sorted union of the inputs, dropping
// blanks. The result is nil when nothing survives, so an empty union stays
// distinguishable from an empty-but-present slice on the wire.
func UnionSorted(lists ...[]string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, ids := range lists {
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}
