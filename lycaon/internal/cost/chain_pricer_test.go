package cost

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/pricing"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubLive struct {
	rate pricing.Rate
	ok   bool
}

func (s stubLive) DiscoveredRate(_, _ string) (pricing.Rate, bool) {
	return s.rate, s.ok
}

func TestChainLiveBeatsFeed(t *testing.T) {
	live := pricing.Rate{InputPer1K: new(float64(0.01)), OutputPer1K: new(float64(0.02)), Currency: "USD"}
	feed := pricing.RateTable{
		Rates: map[pricing.RateKey]pricing.Rate{
			{Kind: "openai", ModelID: "gpt-4o"}: {InputPer1K: new(float64(9)), OutputPer1K: new(float64(9)), Currency: "USD"},
		},
	}
	p := ChainPricer{
		Live:   stubLive{rate: live, ok: true},
		Source: &PricedSource{ID: "models-dev", Table: feed},
	}
	est, err := p.EstimateCost("openai", "gpt-4o", TokenUsage{PromptTokens: 1000, CompletionTokens: 1000})
	testutil.FailErr(t, "EstimateCost", err)
	if est.Unpriced || est.PricingSource != "live" {
		t.Fatalf("est = %+v", est)
	}
	want := 0.01 + 0.02
	if est.EstimatedUSD != want {
		t.Fatalf("usd = %v want %v", est.EstimatedUSD, want)
	}
}

func TestChainSelectedFeed(t *testing.T) {
	feed := pricing.RateTable{
		Rates: map[pricing.RateKey]pricing.Rate{
			{Kind: "openai", ModelID: "gpt-4o"}: {InputPer1K: new(float64(0.001)), OutputPer1K: new(float64(0.002)), Currency: "USD"},
		},
		SourceLastUpdated: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	p := ChainPricer{
		Source: &PricedSource{ID: "litellm", Table: feed},
	}
	est, err := p.EstimateCost("openai", "gpt-4o", TokenUsage{PromptTokens: 1000, CompletionTokens: 0})
	testutil.FailErr(t, "EstimateCost", err)
	if est.PricingSource != "litellm" || est.EstimatedUSD != 0.001 {
		t.Fatalf("est = %+v", est)
	}
}

func TestChainCatalogHintUnpriced(t *testing.T) {
	p := ChainPricer{Live: stubLive{ok: false}}
	est, err := p.EstimateCost("openai", "gpt-4o", TokenUsage{PromptTokens: 1000})
	testutil.FailErr(t, "EstimateCost", err)
	if !est.Unpriced || est.EstimatedUSD != 0 {
		t.Fatalf("est = %+v", est)
	}
}

func TestChainRejectsUnsafeLiveRates(t *testing.T) {
	for _, rate := range []pricing.Rate{
		{InputPer1K: new(float64(-0.01)), Currency: "USD"},
		{InputPer1K: new(float64(math.NaN())), Currency: "USD"},
		{InputPer1K: new(float64(0.01)), Currency: "EUR"},
	} {
		p := ChainPricer{Live: stubLive{rate: rate, ok: true}}
		est, err := p.EstimateCost("openai", "gpt-4o", TokenUsage{PromptTokens: 1000})
		testutil.FailErr(t, "EstimateCost", err)
		if !est.Unpriced || est.EstimatedUSD != 0 {
			t.Fatalf("unsafe rate must be unpriced: rate=%+v est=%+v", rate, est)
		}
	}
}

func TestApplyRateDistinguishesZeroFromMissingCachePrice(t *testing.T) {
	input, output, zero := 0.01, 0.02, 0.0
	usage := TokenUsage{PromptTokens: 1500, CompletionTokens: 1000, CacheReadInputTokens: 500}
	want := input + output

	priced := ApplyRate(pricing.Rate{
		InputPer1K: &input, OutputPer1K: &output, CacheReadPer1K: &zero, Currency: "USD",
	}, usage)
	if priced.Unpriced || priced.EstimatedUSD != want {
		t.Fatalf("known zero cache rate = %+v", priced)
	}

	partial := ApplyRate(pricing.Rate{
		InputPer1K: &input, OutputPer1K: &output, Currency: "USD",
	}, usage)
	if !partial.Unpriced || partial.EstimatedUSD != want {
		t.Fatalf("missing cache rate = %+v", partial)
	}
}

func TestChainHasExactlyOneSelectedSource(t *testing.T) {
	feed := pricing.RateTable{
		Rates: map[pricing.RateKey]pricing.Rate{
			{Kind: "openai", ModelID: "gpt-4o"}: {InputPer1K: new(float64(0.1)), OutputPer1K: new(float64(0.2)), Currency: "USD"},
		},
	}
	p := ChainPricer{Source: &PricedSource{ID: "a", Table: feed}}
	est, err := p.EstimateCost("openai", "gpt-4o", TokenUsage{PromptTokens: 1000, CompletionTokens: 1000})
	testutil.FailErr(t, "EstimateCost", err)
	if est.PricingSource != "a" || est.EstimatedUSD < 0.29 || est.EstimatedUSD > 0.31 {
		t.Fatalf("must use selected feed only: %+v", est)
	}
}

func TestChainInstanceResolvesToKind(t *testing.T) {
	feed := pricing.RateTable{
		Rates: map[pricing.RateKey]pricing.Rate{
			{Kind: "fireworks", ModelID: "accounts/fireworks/models/kimi-k2p7-code"}: {
				InputPer1K: new(float64(0.00095)), OutputPer1K: new(float64(0.004)), Currency: "USD",
			},
		},
	}
	p := NewChainPricer(nil, stubKindMap{"fireworks-1": "fireworks"}, &PricedSource{ID: "models-dev", Table: feed})
	est, err := p.EstimateCost("fireworks-1", "accounts/fireworks/models/kimi-k2p7-code", TokenUsage{
		PromptTokens: 1000, CompletionTokens: 1000,
	})
	testutil.FailErr(t, "EstimateCost", err)
	if est.Unpriced || est.PricingSource != "models-dev" {
		t.Fatalf("est = %+v", est)
	}
	want := 0.00095 + 0.004
	if est.EstimatedUSD != want {
		t.Fatalf("usd = %v want %v", est.EstimatedUSD, want)
	}
}

type stubKindMap map[string]string

func (m stubKindMap) ProviderKind(id string) (string, bool) {
	k, ok := m[id]
	return k, ok
}

func (m stubKindMap) PricedAsModelID(_, model string) string { return model }

func (m stubKindMap) LocalFree(string) bool { return false }

// stubPricedAs maps model IDs to pricing IDs.
type stubPricedAs struct {
	kind string
	from string
	to   string
}

func (s stubPricedAs) ProviderKind(string) (string, bool) { return s.kind, true }

func (s stubPricedAs) PricedAsModelID(_, model string) string {
	if model == s.from {
		return s.to
	}
	return model
}

func (s stubPricedAs) LocalFree(string) bool { return false }

// stubLocalFree marks every provider as no-charge.
type stubLocalFree struct{}

func (stubLocalFree) ProviderKind(string) (string, bool)     { return "ollama", true }
func (stubLocalFree) PricedAsModelID(_, model string) string { return model }
func (stubLocalFree) LocalFree(string) bool                  { return true }

func TestChainLocalFreePricesZero(t *testing.T) {
	p := NewChainPricer(nil, stubLocalFree{}, nil)
	est, err := p.EstimateCost("ollama-1", "llama3.2:8b", TokenUsage{
		PromptTokens: 5000, CompletionTokens: 2000,
	})
	testutil.FailErr(t, "EstimateCost", err)
	if est.Unpriced {
		t.Fatalf("local engines must price, not fall through to Unpriced: %+v", est)
	}
	if est.EstimatedUSD != 0 || est.PricingSource != "local" {
		t.Fatalf("est = %+v, want $0 with source local", est)
	}
}

func TestChainDateSuffixFallback(t *testing.T) {
	feed := pricing.RateTable{
		Rates: map[pricing.RateKey]pricing.Rate{
			{Kind: "anthropic", ModelID: "claude-sonnet-4"}: {InputPer1K: new(float64(0.003)), OutputPer1K: new(float64(0.015)), Currency: "USD"},
		},
	}
	p := NewChainPricer(nil, stubKindMap{"anthropic-1": "anthropic"}, &PricedSource{ID: "models-dev", Table: feed})
	est, err := p.EstimateCost("anthropic-1", "claude-sonnet-4-20250514", TokenUsage{
		PromptTokens: 1000, CompletionTokens: 1000,
	})
	testutil.FailErr(t, "EstimateCost", err)
	if est.Unpriced || est.PricingSource != "models-dev" {
		t.Fatalf("est = %+v", est)
	}
}

func TestChainPricedAsResolution(t *testing.T) {
	feed := pricing.RateTable{
		Rates: map[pricing.RateKey]pricing.Rate{
			{Kind: "bedrock", ModelID: "anthropic.claude-sonnet-4-20250514-v1:0"}: {
				InputPer1K: new(float64(0.003)), OutputPer1K: new(float64(0.015)), Currency: "USD",
			},
		},
	}
	// The mapped ID is the only pricing key.
	p := NewChainPricer(nil, stubPricedAs{
		kind: "bedrock",
		from: "my-app-profile",
		to:   "anthropic.claude-sonnet-4-20250514-v1:0",
	}, &PricedSource{ID: "models-dev", Table: feed})
	est, err := p.EstimateCost("bedrock-1", "my-app-profile", TokenUsage{
		PromptTokens: 1000, CompletionTokens: 1000,
	})
	testutil.FailErr(t, "EstimateCost", err)
	if est.Unpriced || est.PricingSource != "models-dev" {
		t.Fatalf("est = %+v", est)
	}
}

func TestChainGeminiResourcePrefixNormalized(t *testing.T) {
	feed := pricing.RateTable{
		Rates: map[pricing.RateKey]pricing.Rate{
			{Kind: "gemini", ModelID: "gemini-2.5-pro"}: {InputPer1K: new(float64(0.001)), OutputPer1K: new(float64(0.002)), Currency: "USD"},
		},
	}
	p := NewChainPricer(nil, stubKindMap{"gemini-1": "gemini"}, &PricedSource{ID: "models-dev", Table: feed})
	est, err := p.EstimateCost("gemini-1", "models/gemini-2.5-pro", TokenUsage{
		PromptTokens: 1000, CompletionTokens: 1000,
	})
	testutil.FailErr(t, "EstimateCost", err)
	if est.Unpriced {
		t.Fatalf("est = %+v", est)
	}
}

func TestSummaryCarriesPricingMeta(t *testing.T) {
	tracker, _ := newTestTracker(t, NoopPricer{})
	nano := int64(420_000_000)
	asOf := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	testutil.FailErr(t, "RecordUsage", tracker.RecordUsage(context.Background(), UsageEvent{
		SessionID:        "s1",
		Caller:           CallerCoordinator,
		PromptTokens:     10,
		EstimatedNanoUSD: &nano,
		PricingSource:    "models-dev",
		PricedAsOf:       asOf,
	}))
	summary, err := tracker.Summary(context.Background(), api.CostScopeSession, "s1", "")
	testutil.FailErr(t, "Summary", err)
	if summary.EstimateCoverage != api.CostEstimateComplete || len(summary.PricingProvenance) != 1 || summary.PricingProvenance[0].Source != "models-dev" || summary.PricingProvenance[0].PricedAt == nil {
		t.Fatalf("summary = %+v", summary)
	}
	if !summary.PricingProvenance[0].PricedAt.Equal(asOf) {
		t.Fatalf("priced_at = %v", summary.PricingProvenance[0].PricedAt)
	}
}
