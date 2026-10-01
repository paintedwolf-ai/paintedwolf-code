package cost

import (
	"math"
	"testing"

	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/internal/pricing"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCachePricesDistinguishFreeMissingAndPremium(t *testing.T) {
	rate := pricing.Rate{Currency: "USD", InputPer1K: new(float64(1)), OutputPer1K: new(float64(2)),
		CacheReadPer1K: new(float64(0)), CacheWrite1HPer1K: new(float64(2))}
	u := TokenUsage{PromptTokens: 1000, CompletionTokens: 100, CacheReadInputTokens: 600,
		CacheCreationInputTokens: 200, CacheCreation1HInputTokens: 50}
	est := ApplyRate(rate, u)
	if !est.Unpriced || est.UnpricedTokens != 150 || est.PricedTokens != 950 || math.Abs(est.EstimatedUSD-0.5) > 1e-9 {
		t.Fatalf("partial estimate = %+v", est)
	}
	if est.KnownUSD() == nil {
		t.Fatal("partial known subtotal was discarded")
	}
	rate.CacheReadPer1K = nil
	est = ApplyRate(rate, u)
	if est.UnpricedTokens != 750 {
		t.Fatalf("missing read price: %+v", est)
	}
	rate.CacheReadPer1K = new(float64(0.1))
	rate.CacheWritePer1K = new(float64(1.25))
	est = ApplyRate(rate, u)
	if est.Unpriced || math.Abs(est.EstimatedUSD-0.7475) > 1e-9 {
		t.Fatalf("complete estimate = %+v", est)
	}
}

func TestCacheCountsMustBeDisjointSubsets(t *testing.T) {
	rate := pricing.Rate{Currency: "USD", InputPer1K: new(float64(1))}
	for _, usage := range []TokenUsage{
		{PromptTokens: 100, CacheReadInputTokens: 90, CacheCreationInputTokens: 20},
		{PromptTokens: 100, CacheCreationInputTokens: 20, CacheCreation1HInputTokens: 21},
		{PromptTokens: -1}, {PromptTokens: 100, CompletionTokens: -1},
	} {
		est := ApplyRate(rate, usage)
		if !est.Unpriced || est.KnownUSD() != nil {
			t.Fatalf("contradictory usage priced: %+v", est)
		}
	}
}

func TestPromptTierUsesInclusiveInputForWholeCall(t *testing.T) {
	doc, err := modelfeed.ParseDocument([]byte(`{"openai":{"models":{"m":{"cost":{"input":1,"output":2,"cache_read":0.1,"context_over_200k":{"input":2,"output":4,"cache_read":0.2}}}}}}`))
	testutil.FailErr(t, "parse tiered prices", err)
	table := pricing.RateTableFromDocument(doc)
	rate := table.Rates[pricing.RateKey{Kind: "openai", ModelID: "m"}]
	u := TokenUsage{PromptTokens: 200000, CacheReadInputTokens: 190000, CompletionTokens: 1000}
	before := ApplyRate(rate, u)
	u.PromptTokens++
	after := ApplyRate(rate, u)
	if math.Abs(before.EstimatedUSD-0.031) > 1e-9 || math.Abs(after.EstimatedUSD-0.062002) > 1e-9 {
		t.Fatalf("tier estimates: before=%+v after=%+v", before, after)
	}
	if after.RateSnapshot == nil || *after.RateSnapshot.InputPer1K != 0.002 {
		t.Fatal("receipt must retain the selected tier")
	}
}

func TestIncompleteLivePricesDoNotShadowCompleteFeed(t *testing.T) {
	live := pricing.Rate{Currency: "USD", InputPer1K: new(float64(1))}
	feed := pricing.Rate{Currency: "USD", InputPer1K: new(float64(2)), CacheReadPer1K: new(float64(0))}
	p := ChainPricer{Live: stubLive{live, true}, Source: &PricedSource{ID: "feed", Table: pricing.RateTable{
		Rates: map[pricing.RateKey]pricing.Rate{{Kind: "openai", ModelID: "m"}: feed}}}}
	est, err := p.EstimateCost("openai", "m", TokenUsage{PromptTokens: 100, CacheReadInputTokens: 90})
	testutil.FailErr(t, "estimate cached usage", err)
	if est.Unpriced || est.PricingSource != "feed" || est.EstimatedUSD != 0.02 {
		t.Fatalf("estimate = %+v", est)
	}
}

func TestCacheComparisonIncludesWritePremiumsAndMissingPrices(t *testing.T) {
	rate := pricing.Rate{Currency: "USD", InputPer1K: new(float64(1)), CacheWritePer1K: new(float64(1.25))}
	usage := TokenUsage{PromptTokens: 1000, CacheCreationInputTokens: 1000}
	est := ApplyRate(rate, usage)
	if est.CacheSavingsUSD != -0.25 || est.UnpricedCacheTokens != 0 {
		t.Fatalf("write premium = %+v", est)
	}
	usage.PromptTokens += 500
	usage.CacheReadInputTokens = 500
	est = ApplyRate(rate, usage)
	if est.CacheSavingsUSD != -0.25 || est.UnpricedCacheTokens != 500 {
		t.Fatalf("partial comparison = %+v", est)
	}
	rate.CacheReadPer1K = new(float64(0))
	est = ApplyRate(rate, usage)
	if est.CacheSavingsUSD != 0.25 || est.UnpricedCacheTokens != 0 {
		t.Fatalf("read benefit = %+v", est)
	}
}
