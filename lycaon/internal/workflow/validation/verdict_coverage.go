package validation

import (
	"encoding/json"
	"fmt"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	"io"
	"sort"
	"strings"
)

// ParseVerdictCoverage reads the single coverage_review field declared by a phase.
func ParseVerdictCoverage(def workflowdef.ReviewLoopDef, verdict map[string]string) (*api.CoverageReview, error) {
	var out *api.CoverageReview
	for field, kind := range def.VerdictSchema {
		if kind != workflowdef.VerdictCoverageType {
			continue
		}
		if out != nil {
			return nil, fmt.Errorf("verdict declares multiple coverage reviews")
		}
		dec := json.NewDecoder(strings.NewReader(verdict[field]))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&out); err != nil {
			return nil, fmt.Errorf("coverage review: %w", err)
		}
		if out == nil {
			return nil, fmt.Errorf("coverage review must be an object")
		}
		if err := dec.Decode(&struct{}{}); err != io.EOF {
			return nil, fmt.Errorf("coverage review must contain one object")
		}
	}
	return out, nil
}

func SortedClaimFields(byField map[string][]VerdictClaim) []string {
	names := make([]string, 0, len(byField))
	for name := range byField {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
