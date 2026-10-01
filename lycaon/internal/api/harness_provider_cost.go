package api

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/pricing"
)

type probeCostEstimator interface {
	Estimate(context.Context, string, string, cost.TokenUsage) (cost.CostEstimate, error)
}

type harnessProbeCost struct {
	EstimatedUSD *float64      `json:"estimated_usd"`
	Complete     bool          `json:"complete"`
	Source       string        `json:"source,omitempty"`
	AsOf         *time.Time    `json:"as_of,omitempty"`
	Rate         *pricing.Rate `json:"rate,omitempty"`
}

func estimateProbeCosts(ctx context.Context, estimator probeCostEstimator, provider, model string, stages []llm.ConversationProbe) map[string]harnessProbeCost {
	quotes := make(map[string]harnessProbeCost, len(stages))
	for _, stage := range stages {
		quotes[stage.Stage] = estimateProbeCost(ctx, estimator, provider, model, stage.Usage)
	}
	return quotes
}

func estimateProbeCost(ctx context.Context, estimator probeCostEstimator, provider, model string, usage modelcall.TokenUsage) harnessProbeCost {
	if estimator == nil || !usage.Present {
		return harnessProbeCost{}
	}
	tokens := cost.TokenUsage{
		PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
		CacheReadInputTokens: usage.CacheReadInputTokens, CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheCreation1HInputTokens: usage.CacheCreation1HInputTokens,
	}
	if !tokens.Valid() {
		return harnessProbeCost{}
	}
	estimate, err := estimator.Estimate(ctx, provider, model, tokens)
	if err != nil || estimate.Currency != "USD" {
		return harnessProbeCost{}
	}
	quote := harnessProbeCost{EstimatedUSD: estimate.KnownUSD(), Complete: !estimate.Unpriced && !usage.Incomplete,
		Source: estimate.PricingSource, Rate: estimate.RateSnapshot}
	if !estimate.PricedAsOf.IsZero() {
		quote.AsOf = &estimate.PricedAsOf
	}
	return quote
}
