package pricing

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/modelfeed"
)

// dialectSlugToKind maps pricing dialect slugs to catalog provider kinds.
var dialectSlugToKind = map[string]string{
	"fireworks_ai": "fireworks",
	"together_ai":  "together",
	"bedrock":      "bedrock",
	"vertex_ai":    "vertex",
	"google_ai":    "gemini",
}

// KindForExternalSlug resolves shared feed keys before pricing dialect slugs.
func KindForExternalSlug(slug string) (string, bool) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return "", false
	}
	if kind, ok := modelfeed.KindForFeedKey(slug); ok {
		return kind, true
	}
	if kind, ok := dialectSlugToKind[slug]; ok {
		if _, mapped := modelfeed.FeedKeyForKind(kind); mapped {
			return kind, true
		}
	}
	if _, ok := modelfeed.FeedKeyForKind(slug); ok {
		return slug, true
	}
	return "", false
}

// putHostRate indexes prices only for mapped provider kinds and canonical model IDs.
func putHostRate(rates map[RateKey]Rate, providerSlug, apiModelID string, rate Rate) {
	if strings.TrimSpace(apiModelID) == "" || !rateNonEmpty(rate) {
		return
	}
	kind, ok := KindForExternalSlug(providerSlug)
	if !ok {
		return
	}
	apiModelID = NormalizeRateModelID(kind, apiModelID)
	if apiModelID == "" {
		return
	}
	rates[RateKey{Kind: RateKind(kind), ModelID: apiModelID}] = rate
}

func rateNonEmpty(r Rate) bool {
	return r.InputPer1K != nil || r.OutputPer1K != nil || r.CacheReadPer1K != nil || r.CacheWritePer1K != nil || r.CacheWrite1HPer1K != nil
}

func validateRateTable(table RateTable) error {
	if len(table.Rates) == 0 {
		return fmt.Errorf("parsed zero rates")
	}
	for key, rate := range table.Rates {
		if strings.TrimSpace(key.Kind) == "" || strings.TrimSpace(key.ModelID) == "" {
			return fmt.Errorf("rate has empty identity")
		}
		if !strings.EqualFold(strings.TrimSpace(rate.Currency), "USD") {
			return fmt.Errorf("rate for %s/%s has unsupported currency %q", key.Kind, key.ModelID, rate.Currency)
		}
		if !rate.Valid() {
			return fmt.Errorf("rate for %s/%s is invalid or empty", key.Kind, key.ModelID)
		}
	}
	return nil
}

// stripProviderPrefix removes "{slug}/" from a composite feed model key when present.
func stripProviderPrefix(providerSlug, modelKey string) string {
	modelKey = strings.TrimSpace(modelKey)
	providerSlug = strings.TrimSpace(providerSlug)
	if providerSlug == "" || modelKey == "" {
		return modelKey
	}
	prefix := providerSlug + "/"
	if strings.HasPrefix(modelKey, prefix) {
		return strings.TrimPrefix(modelKey, prefix)
	}
	kind, ok := KindForExternalSlug(providerSlug)
	if !ok {
		return modelKey
	}
	if feedKey, ok := modelfeed.FeedKeyForKind(kind); ok {
		fp := feedKey + "/"
		if strings.HasPrefix(modelKey, fp) {
			return strings.TrimPrefix(modelKey, fp)
		}
	}
	for slug, k := range dialectSlugToKind {
		if k != kind {
			continue
		}
		p := slug + "/"
		if strings.HasPrefix(modelKey, p) {
			return strings.TrimPrefix(modelKey, p)
		}
	}
	return modelKey
}
