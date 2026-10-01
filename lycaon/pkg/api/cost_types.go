package api

// --- Extensibility: cost ---

type CostScope string

const (
	CostScopeSession CostScope = "session"
	CostScopeProject CostScope = "project"
)

// CostEstimateCoverage says what EstimatedUSD covers. The host stamps it from
// the call receipts; consumers branch on it rather than re-deriving it from
// the counters.
type CostEstimateCoverage string

const (
	// CostEstimateComplete: every incurred token was reported and priced.
	CostEstimateComplete CostEstimateCoverage = "complete"
	// CostEstimateLowerBound: some usage had no price, or a charged provider
	// call ended without a usage report. The true estimate is at least the figure.
	CostEstimateLowerBound CostEstimateCoverage = "lower_bound"
	// CostEstimateUnpriced: usage exists but none of it could be priced.
	CostEstimateUnpriced CostEstimateCoverage = "unpriced"
)

// Priced reports whether EstimatedUSD carries any priced usage.
func (s CostSummary) Priced() bool {
	return s.EstimateCoverage != CostEstimateUnpriced
}
