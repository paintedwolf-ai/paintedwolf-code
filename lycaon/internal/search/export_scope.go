package search

import "strings"

// PlanSARIFScoped reports whether the compiled plan may export SARIF (scan findings only).
func PlanSARIFScoped(plan *RoutedPlan) bool {
	if plan == nil || plan.Code != nil || plan.Store == nil {
		return false
	}
	scanScoped := false
	for _, f := range plan.Interpretation.Filters {
		if f.Negated {
			continue
		}
		switch {
		case f.Field == "kind" && strings.EqualFold(f.Value, scanKind),
			f.Field == "shape" && strings.EqualFold(f.Value, "artifact"):
			scanScoped = true
		case f.Field == "kind":
			return false
		}
	}
	if len(plan.Interpretation.FTSTerms) > 0 && !scanScoped {
		return false
	}
	return scanScoped
}

// HitsSARIFEligible reports whether every hit can map to a scan finding.
func HitsSARIFEligible(hits []Hit) bool {
	for _, hit := range hits {
		if !hitSARIFEligible(hit) {
			return false
		}
	}
	return true
}

func hitSARIFEligible(hit Hit) bool {
	kind := strings.TrimSpace(hit.HitKind)
	source := strings.TrimSpace(hit.Source)
	return kind == HitKindFinding || source == SourceFinding
}
