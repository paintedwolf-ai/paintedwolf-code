package cost

import (
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/pricing"
)

// LiveRateSource supplies rates discovered from a provider.
type LiveRateSource interface {
	DiscoveredRate(providerID, model string) (pricing.Rate, bool)
}

// KindResolver maps provider instances and model IDs to pricing keys.
type KindResolver interface {
	ProviderKind(instanceID string) (kind string, ok bool)
	// PricedAsModelID returns the model ID used by pricing tables.
	PricedAsModelID(instanceID, model string) string
	// LocalFree reports whether the provider has no token charge.
	LocalFree(instanceID string) bool
}

// PricedSource is the selected ready feed table.
type PricedSource struct {
	ID    string
	Table pricing.RateTable
}

// ChainPricer prefers complete coverage, then live rates over feed rates.
type ChainPricer struct {
	Live   LiveRateSource
	Kinds  KindResolver
	Source *PricedSource
}

// NoopPricer always returns Unpriced estimates without classifying providers.
type NoopPricer struct{}

// TrackingDisabledPricer classifies no-charge providers without estimating.
type TrackingDisabledPricer struct {
	Kinds KindResolver
}

// NewChainPricer builds a pricing chain.
func NewChainPricer(live LiveRateSource, kinds KindResolver, source *PricedSource) ChainPricer {
	return ChainPricer{
		Live:   live,
		Kinds:  kinds,
		Source: source,
	}
}

// EstimateCost implements Pricer.
func (NoopPricer) EstimateCost(_, _ string, _ TokenUsage) (CostEstimate, error) {
	return CostEstimate{Currency: "USD", Unpriced: true}, nil
}

// NoCharge implements Pricer.
func (NoopPricer) NoCharge(_, _ string) bool { return false }

func (TrackingDisabledPricer) EstimateCost(_, _ string, _ TokenUsage) (CostEstimate, error) {
	return CostEstimate{Currency: "USD", Unpriced: true}, nil
}

func (p TrackingDisabledPricer) NoCharge(providerID, _ string) bool {
	return p.Kinds != nil && p.Kinds.LocalFree(providerID)
}

// NoCharge implements Pricer.
func (p ChainPricer) NoCharge(providerID, _ string) bool {
	return p.Kinds != nil && p.Kinds.LocalFree(providerID)
}

// EstimateCost implements Pricer.
func (p ChainPricer) EstimateCost(providerID, model string, usage TokenUsage) (CostEstimate, error) {
	if p.Kinds != nil && p.Kinds.LocalFree(providerID) {
		est := CostEstimate{Currency: "USD", PricingSource: "local"}
		logCostEstimate(providerID, model, providerID, model, pricing.Rate{}, usage, est)
		return est, nil
	}
	var liveEstimate *CostEstimate
	if p.Live != nil {
		if rate, ok := p.Live.DiscoveredRate(providerID, model); ok && rate.Valid() {
			est := ApplyRate(rate, usage)
			est.PricingSource = "live"
			logCostEstimate(providerID, model, providerID, model, rate, usage, est)
			if !est.Unpriced {
				return est, nil
			}
			liveEstimate = &est
		}
	}
	kind := providerID
	lookupModel := model
	if p.Kinds != nil {
		if k, ok := p.Kinds.ProviderKind(providerID); ok && k != "" {
			kind = k
		}
		if mapped := p.Kinds.PricedAsModelID(providerID, model); strings.TrimSpace(mapped) != "" {
			lookupModel = mapped
		}
	}
	if p.Source != nil {
		rate, ok := lookupTableRate(p.Source.Table, kind, lookupModel)
		if ok {
			est := ApplyRate(rate, usage)
			if liveEstimate != nil && liveEstimate.UnpricedTokens <= est.UnpricedTokens {
				return *liveEstimate, nil
			}
			est.PricingSource = p.Source.ID
			if !p.Source.Table.SourceLastUpdated.IsZero() {
				est.PricedAsOf = p.Source.Table.SourceLastUpdated
			} else if !p.Source.Table.FetchedAt.IsZero() {
				est.PricedAsOf = p.Source.Table.FetchedAt
			}
			logCostEstimate(providerID, model, kind, lookupModel, rate, usage, est)
			return est, nil
		}
	}
	if liveEstimate != nil {
		return *liveEstimate, nil
	}
	est := CostEstimate{Currency: "USD", Unpriced: true, UnpricedTokens: max(0, usage.PromptTokens) + max(0, usage.CompletionTokens)}
	logCostEstimate(providerID, model, kind, lookupModel, pricing.Rate{}, usage, est)
	return est, nil
}

func logCostEstimate(providerID, model, kind, lookupModel string, rate pricing.Rate, usage TokenUsage, est CostEstimate) {
	if est.RateSnapshot != nil {
		rate = *est.RateSnapshot
	}
	slog.Debug("cost estimate",
		"component", "cost",
		"provider_id", providerID,
		"model", model,
		"kind", kind,
		"lookup_model", lookupModel,
		"pricing_source", est.PricingSource,
		"unpriced", est.Unpriced,
		"estimated_usd", est.EstimatedUSD,
		"prompt_tokens", usage.PromptTokens,
		"completion_tokens", usage.CompletionTokens,
		"cache_read_tokens", usage.CacheReadInputTokens,
		"cache_write_tokens", usage.CacheCreationInputTokens,
		"cache_write_1h_tokens", usage.CacheCreation1HInputTokens,
		"unpriced_tokens", est.UnpricedTokens,
		"input_per_1k", pricingAmount(rate.InputPer1K),
		"output_per_1k", pricingAmount(rate.OutputPer1K),
		"cache_read_per_1k", pricingAmount(rate.CacheReadPer1K),
		"cache_write_per_1k", pricingAmount(rate.CacheWritePer1K),
	)
}

func lookupTableRate(table pricing.RateTable, kind, model string) (pricing.Rate, bool) {
	if table.Rates == nil {
		return pricing.Rate{}, false
	}
	rateKind := pricing.RateKind(kind)
	for _, candidate := range pricing.RateLookupCandidates(kind, model) {
		if r, ok := table.Rates[pricing.RateKey{Kind: rateKind, ModelID: candidate}]; ok && r.Valid() {
			return r, true
		}
	}
	return pricing.Rate{}, false
}

func pricingAmount(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}
