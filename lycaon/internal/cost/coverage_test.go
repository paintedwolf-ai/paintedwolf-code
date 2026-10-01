package cost

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestEstimateCoverageClassification(t *testing.T) {
	cases := []struct {
		name           string
		anyPriced      bool
		tokens         int
		unpriced       int
		unknownCharged int
		want           api.CostEstimateCoverage
	}{
		{name: "no usage", want: api.CostEstimateComplete},
		{name: "all priced", anyPriced: true, tokens: 100, want: api.CostEstimateComplete},
		{name: "nothing priced", tokens: 100, unpriced: 100, want: api.CostEstimateUnpriced},
		{name: "priced with unpriced remainder", anyPriced: true, tokens: 100, unpriced: 40, want: api.CostEstimateLowerBound},
		{name: "priced with unreported charged call", anyPriced: true, tokens: 100, unknownCharged: 1, want: api.CostEstimateLowerBound},
		// A charged call that died before any usage landed is still money the figure omits.
		{name: "only an unreported charged call", unknownCharged: 1, want: api.CostEstimateLowerBound},
		{name: "nothing priced and an unreported call", tokens: 100, unpriced: 100, unknownCharged: 1, want: api.CostEstimateUnpriced},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := estimateCoverage(tc.anyPriced, tc.tokens, tc.unpriced, tc.unknownCharged)
			if got != tc.want {
				t.Fatalf("estimateCoverage = %q want %q", got, tc.want)
			}
		})
	}
}
