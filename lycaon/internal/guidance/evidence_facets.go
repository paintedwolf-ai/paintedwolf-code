package guidance

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// DistinctEvidenceFacetValues collects unique non-empty facet values from wire
// evidence records. Column is "kind" or "shape" — open strings for Explorer
// facets; values are discovered from data, never a closed enum.
func DistinctEvidenceFacetValues(records []api.CitationGroundingEvidenceRecord, column string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, rec := range records {
		var val string
		switch strings.TrimSpace(column) {
		case "kind":
			val = strings.TrimSpace(rec.Kind)
		case "shape":
			val = strings.TrimSpace(rec.Shape)
		default:
			return nil
		}
		if val == "" {
			continue
		}
		if _, ok := seen[val]; ok {
			continue
		}
		seen[val] = struct{}{}
		out = append(out, val)
	}
	sort.Strings(out)
	return out
}
