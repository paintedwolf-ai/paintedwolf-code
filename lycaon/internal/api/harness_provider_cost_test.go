package api

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/pricing"
)

type probeEstimateFunc func(context.Context, string, string, cost.TokenUsage) (cost.CostEstimate, error)

func (f probeEstimateFunc) Estimate(ctx context.Context, provider, model string, usage cost.TokenUsage) (cost.CostEstimate, error) {
	return f(ctx, provider, model, usage)
}

func TestProbeCostsUseApplicationEstimatorAndAllCacheBuckets(t *testing.T) {
	input, output, read, write, hour := 2.0, 10.0, 0.2, 3.0, 4.0
	rate := pricing.Rate{Currency: "USD", InputPer1K: &input, OutputPer1K: &output,
		CacheReadPer1K: &read, CacheWritePer1K: &write, CacheWrite1HPer1K: &hour}
	observed := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	estimator := probeEstimateFunc(func(_ context.Context, provider, model string, usage cost.TokenUsage) (cost.CostEstimate, error) {
		if provider != "fixture-provider" || model != "fixture-model" {
			t.Fatalf("unexpected estimate identity: %s/%s", provider, model)
		}
		estimate := cost.ApplyRate(rate, usage)
		estimate.PricingSource, estimate.PricedAsOf = "fixture", observed
		return estimate, nil
	})
	stages := []llm.ConversationProbe{{Stage: "imported_tool_results", Usage: modelcall.TokenUsage{
		Present: true, PromptTokens: 1000, CompletionTokens: 100, CacheReadInputTokens: 600,
		CacheCreationInputTokens: 200, CacheCreation1HInputTokens: 100,
	}}}
	quote := estimateProbeCosts(t.Context(), estimator, "fixture-provider", "fixture-model", stages)[stages[0].Stage]
	if !quote.Complete || quote.EstimatedUSD == nil || math.Abs(*quote.EstimatedUSD-2.22) > 1e-9 {
		t.Fatalf("cache-inclusive quote: %+v", quote)
	}
	if quote.Source != "fixture" || quote.AsOf == nil || !quote.AsOf.Equal(observed) || quote.Rate == nil {
		t.Fatalf("missing pricing provenance: %+v", quote)
	}
	stages[0].Usage.Incomplete = true
	quote = estimateProbeCosts(t.Context(), estimator, "fixture-provider", "fixture-model", stages)[stages[0].Stage]
	if quote.Complete || quote.EstimatedUSD == nil {
		t.Fatalf("partial usage must retain a lower bound: %+v", quote)
	}
}

func TestProbeCostsKeepUnavailableAndExplicitlyFreeQuotesDistinct(t *testing.T) {
	usage := modelcall.TokenUsage{Present: true, PromptTokens: 10}
	for _, test := range []struct {
		name     string
		estimate cost.CostEstimate
		err      error
		complete bool
	}{
		{name: "free", estimate: cost.CostEstimate{Currency: "USD", PricedTokens: 10}, complete: true},
		{name: "unknown", estimate: cost.CostEstimate{Currency: "USD", Unpriced: true, UnpricedTokens: 10}},
		{name: "error", err: errors.New("pricing unavailable")},
		{name: "other currency", estimate: cost.CostEstimate{Currency: "EUR", PricedTokens: 10}},
	} {
		t.Run(test.name, func(t *testing.T) {
			estimator := probeEstimateFunc(func(context.Context, string, string, cost.TokenUsage) (cost.CostEstimate, error) {
				return test.estimate, test.err
			})
			quote := estimateProbeCost(t.Context(), estimator, "provider", "model", usage)
			if quote.Complete != test.complete || (quote.EstimatedUSD != nil) != test.complete {
				t.Fatalf("unexpected quote completeness: %+v", quote)
			}
		})
	}
	for _, unavailable := range []modelcall.TokenUsage{{}, {Present: true, PromptTokens: 1, CacheReadInputTokens: 2}} {
		estimator := probeEstimateFunc(func(context.Context, string, string, cost.TokenUsage) (cost.CostEstimate, error) {
			t.Fatal("invalid or absent usage reached estimator")
			return cost.CostEstimate{}, nil
		})
		if quote := estimateProbeCost(t.Context(), estimator, "provider", "model", unavailable); quote.Complete || quote.EstimatedUSD != nil {
			t.Fatalf("invalid usage produced a quote: %+v", quote)
		}
	}
}
