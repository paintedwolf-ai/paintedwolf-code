package pricing

import (
	"math"
	"strings"
)

// Valid accepts explicit zero prices and rejects empty or malformed rates.
func (r Rate) Valid() bool {
	if r.Currency != "" && !strings.EqualFold(r.Currency, "USD") {
		return false
	}
	anyPrice := false
	for _, value := range []*float64{r.InputPer1K, r.OutputPer1K, r.CacheReadPer1K, r.CacheWritePer1K, r.CacheWrite1HPer1K} {
		if value == nil {
			continue
		}
		if *value < 0 || math.IsNaN(*value) || math.IsInf(*value, 0) {
			return false
		}
		anyPrice = true
	}
	seen := make(map[int]bool)
	for _, tier := range r.Tiers {
		if tier.AboveTokens <= 0 || seen[tier.AboveTokens] || len(tier.Rate.Tiers) != 0 || !tier.Rate.Valid() {
			return false
		}
		seen[tier.AboveTokens] = true
	}
	return anyPrice
}
