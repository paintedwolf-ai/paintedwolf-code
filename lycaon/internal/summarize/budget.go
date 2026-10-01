package summarize

import (
	"github.com/lycaon/lycaon/internal/tokenest"
)

// EstimateTokens returns the deterministic size proxy for s: rune count divided
// by SizeDivisor (ceiling). Non-empty strings cost at least 1 so short identity
// / skeleton lines cannot admit unbounded rows under a finite budget.
func (c Caps) EstimateTokens(s string) int {
	return tokenest.Estimate(s, c.Pack.SizeDivisor)
}
