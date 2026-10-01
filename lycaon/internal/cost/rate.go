package cost

import (
	"math"

	"github.com/lycaon/lycaon/internal/pricing"
)

// ApplyRate prices disjoint token buckets and preserves unknown prices.
func ApplyRate(rate pricing.Rate, usage TokenUsage) CostEstimate {
	out := CostEstimate{Currency: "USD"}
	if !rate.Valid() || !usage.Valid() {
		out.Unpriced = true
		out.UnpricedTokens = max(0, usage.PromptTokens) + max(0, usage.CompletionTokens)
		out.UnpricedCacheTokens = max(0, usage.CacheReadInputTokens) + max(0, usage.CacheCreationInputTokens)
		return out
	}
	r := rate.ForPrompt(usage.PromptTokens)
	out.RateSnapshot = &r
	fresh := usage.PromptTokens - usage.CacheReadInputTokens - usage.CacheCreationInputTokens
	buckets := []struct {
		tokens int
		price  *float64
	}{
		{fresh, r.InputPer1K},
		{usage.CompletionTokens, r.OutputPer1K},
		{usage.CacheReadInputTokens, r.CacheReadPer1K},
		{usage.CacheCreationInputTokens - usage.CacheCreation1HInputTokens, r.CacheWritePer1K},
		{usage.CacheCreation1HInputTokens, r.CacheWrite1HPer1K},
	}
	for _, bucket := range buckets {
		if bucket.price == nil {
			out.UnpricedTokens += bucket.tokens
		} else {
			out.PricedTokens += bucket.tokens
			out.EstimatedUSD += float64(bucket.tokens) * *bucket.price / 1000
		}
	}
	out.CacheSavingsUSD, out.UnpricedCacheTokens = cacheComparison(r, usage)
	out.Unpriced = out.UnpricedTokens > 0
	return out
}

// Valid reports whether token buckets are consistent.
func (u TokenUsage) Valid() bool {
	return u.PromptTokens >= 0 && u.CompletionTokens >= 0 && u.CacheReadInputTokens >= 0 &&
		u.CacheCreationInputTokens >= 0 && u.CacheCreation1HInputTokens >= 0 &&
		u.CacheReadInputTokens <= u.PromptTokens && u.CacheCreationInputTokens <= u.PromptTokens-u.CacheReadInputTokens &&
		u.CacheCreation1HInputTokens <= u.CacheCreationInputTokens
}

// KnownUSD includes a partial known subtotal, including an explicitly free one.
func (e CostEstimate) KnownUSD() *float64 {
	if e.Unpriced && e.PricedTokens == 0 {
		return nil
	}
	return &e.EstimatedUSD
}

// NanoUSD converts the known estimate and the signed cache comparison to the
// integer nano-dollars usage is recorded in.
func (e CostEstimate) NanoUSD() (estimated *int64, cacheSavings int64, err error) {
	if usd := e.KnownUSD(); usd != nil {
		nano, err := USDToNano(*usd)
		if err != nil {
			return nil, 0, err
		}
		estimated = &nano
	}
	cacheSavings, err = USDToNano(math.Abs(e.CacheSavingsUSD))
	if err != nil {
		return nil, 0, err
	}
	if e.CacheSavingsUSD < 0 {
		cacheSavings = -cacheSavings
	}
	return estimated, cacheSavings, nil
}

func cacheComparison(r pricing.Rate, u TokenUsage) (float64, int) {
	var savings float64
	var unpriced int
	for _, bucket := range []struct {
		tokens int
		price  *float64
	}{
		{u.CacheReadInputTokens, r.CacheReadPer1K},
		{u.CacheCreationInputTokens - u.CacheCreation1HInputTokens, r.CacheWritePer1K},
		{u.CacheCreation1HInputTokens, r.CacheWrite1HPer1K},
	} {
		if r.InputPer1K == nil || bucket.price == nil {
			unpriced += bucket.tokens
			continue
		}
		savings += float64(bucket.tokens) * (*r.InputPer1K - *bucket.price) / 1000
	}
	return savings, unpriced
}
