package pricing

import (
	"encoding/json"
	"fmt"
)

// Per-token prices become per-thousand rates for mapped providers.
func ParseLitellm(body []byte) (RateTable, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return RateTable{}, fmt.Errorf("litellm: decode root: %w", err)
	}
	rates := make(map[RateKey]Rate)
	for key, blob := range root {
		if key == "sample_spec" {
			continue
		}
		var row struct {
			LitellmProvider string `json:"litellm_provider"`
		}
		if err := json.Unmarshal(blob, &row); err != nil {
			continue
		}
		rate, err := ParseLiteLLMRate(blob)
		if err != nil {
			return RateTable{}, fmt.Errorf("litellm rate %s: %w", key, err)
		}
		if !rateNonEmpty(rate) {
			continue
		}

		modelID := stripProviderPrefix(row.LitellmProvider, key)
		putHostRate(rates, row.LitellmProvider, modelID, rate)
	}
	table := RateTable{Rates: rates, Status: StatusOK}
	if err := validateRateTable(table); err != nil {
		return RateTable{}, fmt.Errorf("litellm: %w", err)
	}
	return table, nil
}
