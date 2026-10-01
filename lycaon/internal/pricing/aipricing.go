package pricing

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type aiPricingRow struct {
	ProviderSlug     string  `json:"provider_slug"`
	ModelName        string  `json:"model_name"`
	CanonicalSlug    string  `json:"canonical_slug"`
	Metric           string  `json:"metric"`
	Unit             string  `json:"unit"`
	PriceNumeric     float64 `json:"price_numeric"`
	LatestObservedAt string  `json:"latest_observed_at"`
	Currency         string  `json:"currency"`
}

type aiPricingPage struct {
	Data   []aiPricingRow `json:"data"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

func decodeAIPricingPage(body []byte) (aiPricingPage, error) {
	var page aiPricingPage
	if err := json.Unmarshal(body, &page); err != nil {
		return aiPricingPage{}, fmt.Errorf("ai-pricing-fyi: decode: %w", err)
	}
	return page, nil
}

// parseAIPricingPages turns per-million prices into per-thousand rates for
// mapped providers.
func parseAIPricingPages(bodies [][]byte) (RateTable, error) {

	type accum struct {
		rate   Rate
		filled bool
		slug   string
		model  string
	}
	byKey := map[string]*accum{}
	var latest time.Time

	for _, body := range bodies {
		page, err := decodeAIPricingPage(body)
		if err != nil {
			return RateTable{}, err
		}
		for _, row := range page.Data {
			if row.Unit != "per_1m_tokens" {
				continue
			}
			modelID := row.ModelName
			if modelID == "" {
				modelID = row.CanonicalSlug
			}
			if modelID == "" || row.ProviderSlug == "" {
				continue
			}
			if _, ok := KindForExternalSlug(row.ProviderSlug); !ok {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(row.Currency), "USD") {
				return RateTable{}, fmt.Errorf("ai-pricing-fyi: unsupported currency %q", row.Currency)
			}
			ak := row.ProviderSlug + "\x00" + modelID
			a := byKey[ak]
			if a == nil {
				a = &accum{rate: Rate{Currency: "USD"}, slug: row.ProviderSlug, model: modelID}
				byKey[ak] = a
			}
			per1k := row.PriceNumeric / 1000
			switch row.Metric {
			case "input_token":
				a.rate.InputPer1K = &per1k
				a.filled = true
			case "output_token":
				a.rate.OutputPer1K = &per1k
				a.filled = true
			case "cached_input_token":
				a.rate.CacheReadPer1K = &per1k
				a.filled = true
			default:
				continue
			}
			if t, err := time.Parse(time.RFC3339Nano, row.LatestObservedAt); err == nil && t.After(latest) {
				latest = t
			} else if t, err := time.Parse(time.RFC3339, row.LatestObservedAt); err == nil && t.After(latest) {
				latest = t
			}
		}
	}

	rates := make(map[RateKey]Rate, len(byKey))
	for _, a := range byKey {
		if a.filled {
			// Some rows carry composite "provider/model" names; key on the bare id.
			putHostRate(rates, a.slug, stripProviderPrefix(a.slug, a.model), a.rate)
		}
	}
	table := RateTable{Rates: rates, SourceLastUpdated: latest, Status: StatusOK}
	if err := validateRateTable(table); err != nil {
		return RateTable{}, fmt.Errorf("ai-pricing-fyi: %w", err)
	}
	return table, nil
}
