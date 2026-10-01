package pricing

import (
	"strings"

	"github.com/lycaon/lycaon/internal/modelfeed"
)

// RateTableFromDocument projects shared metadata for mapped provider kinds.
func RateTableFromDocument(doc *modelfeed.Document) RateTable {
	rates := make(map[RateKey]Rate)
	if doc == nil {
		return RateTable{Rates: rates, Status: StatusOK}
	}
	for _, kind := range modelfeed.MappedKinds() {
		feedKey, ok := modelfeed.FeedKeyForKind(kind)
		if !ok {
			continue
		}
		provider, ok := doc.Provider(feedKey)
		if !ok {
			continue
		}
		for modelKey, model := range provider.Models {
			if model.Cost == nil {
				continue
			}
			modelID := strings.TrimSpace(model.ID)
			if modelID == "" {
				modelID = strings.TrimSpace(modelKey)
			}
			if modelID == "" {
				continue
			}
			r := modelFeedRate(model.Cost)

			if !rateNonEmpty(r) {
				continue
			}
			rates[RateKey{Kind: RateKind(kind), ModelID: NormalizeRateModelID(kind, modelID)}] = r
		}
	}
	out := RateTable{Rates: rates, Status: StatusOK}
	if !doc.FetchedAt.IsZero() {
		out.FetchedAt = doc.FetchedAt
	}
	return out
}

func modelFeedRate(cost *modelfeed.Cost) Rate {
	r := modelFeedBaseRate(cost)
	for _, tier := range cost.Tiers {
		if tier.Tier.Type != "" && tier.Tier.Type != "context" {
			continue
		}
		if tier.Tier.Size == 0 {
			continue
		}
		r.Tiers = append(r.Tiers, PromptTier{AboveTokens: tier.Tier.Size, Rate: modelFeedBaseRate(&tier.Cost)})
	}
	if len(cost.Tiers) == 0 && cost.ContextOver200K != nil {
		r.Tiers = append(r.Tiers, PromptTier{AboveTokens: 200000, Rate: modelFeedBaseRate(cost.ContextOver200K)})
	}
	return r
}

func modelFeedBaseRate(cost *modelfeed.Cost) Rate {
	return Rate{InputPer1K: Scale(cost.Input, 0.001), OutputPer1K: Scale(cost.Output, 0.001),
		CacheReadPer1K: Scale(cost.CacheRead, 0.001), CacheWritePer1K: Scale(cost.CacheWrite, 0.001), Currency: "USD"}
}
