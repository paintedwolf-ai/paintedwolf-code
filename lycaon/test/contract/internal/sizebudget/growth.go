package sizebudget

// EvaluateGrowth permits unchanged or shrinking legacy excess, preserving explicit caps.
// New artifacts and growth above the limit remain failures, including renamed artifacts.
func EvaluateGrowth(policy Policy, base, head Measurements, touched Touched) []Finding {
	findings := Evaluate(policy, head, touched)
	for index := range findings {
		finding := &findings[index]
		previous, existed := base[finding.Category][finding.ID]
		if finding.Kind == OverLimit && existed && finding.Measured <= previous {
			finding.Kind = LegacyDebt
			finding.Previous = &previous
		}
	}
	return findings
}
