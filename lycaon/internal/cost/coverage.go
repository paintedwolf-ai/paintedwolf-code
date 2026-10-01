package cost

import "github.com/lycaon/lycaon/pkg/api"

// estimateCoverage classifies one summary's estimated USD from the receipt
// facts that built it. This is the only place the classification is made;
// the ceiling gate, the ceiling notice, and Den all read the stamped value.
func estimateCoverage(anyPriced bool, tokenTotal, unpricedTokens, unknownChargedCalls int) api.CostEstimateCoverage {
	if tokenTotal > 0 && !anyPriced {
		return api.CostEstimateUnpriced
	}
	if unpricedTokens > 0 || unknownChargedCalls > 0 {
		return api.CostEstimateLowerBound
	}
	return api.CostEstimateComplete
}
