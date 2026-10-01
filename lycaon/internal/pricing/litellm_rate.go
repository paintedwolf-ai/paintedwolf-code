package pricing

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// The feed and model-info API share the same declared token prices.
func ParseLiteLLMRate(blob []byte) (Rate, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(blob, &fields); err != nil {
		return Rate{}, err
	}
	r := Rate{Currency: "USD"}
	tiers := make(map[int]Rate)
	for key, raw := range fields {
		base, threshold, ok := liteLLMPriceKey(key)
		if !ok {
			continue
		}
		var value *float64
		if err := json.Unmarshal(raw, &value); err != nil {
			return Rate{}, fmt.Errorf("%s: %w", key, err)
		}
		if threshold == 0 {
			setLiteLLMPrice(&r, base, Scale(value, 1000))
		} else {
			tier := tiers[threshold]
			tier.Currency = "USD"
			setLiteLLMPrice(&tier, base, Scale(value, 1000))
			tiers[threshold] = tier
		}
	}
	for threshold, tier := range tiers {
		r.Tiers = append(r.Tiers, PromptTier{AboveTokens: threshold, Rate: tier})
	}
	sort.Slice(r.Tiers, func(i, j int) bool { return r.Tiers[i].AboveTokens < r.Tiers[j].AboveTokens })
	return r, nil
}

func liteLLMPriceKey(key string) (string, int, bool) {
	for _, base := range []string{"input_cost_per_token", "output_cost_per_token", "cache_read_input_token_cost", "cache_creation_input_token_cost_above_1hr", "cache_creation_input_token_cost"} {
		if key == base {
			return base, 0, true
		}
		suffix, ok := strings.CutPrefix(key, base+"_above_")
		if !ok {
			continue
		}
		number, ok := strings.CutSuffix(suffix, "k_tokens")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(number)
		if err == nil && n > 0 && n <= 1000000 {
			return base, n * 1000, true
		}
	}
	return "", 0, false
}

func setLiteLLMPrice(r *Rate, key string, value *float64) {
	switch key {
	case "input_cost_per_token":
		r.InputPer1K = value
	case "output_cost_per_token":
		r.OutputPer1K = value
	case "cache_read_input_token_cost":
		r.CacheReadPer1K = value
	case "cache_creation_input_token_cost":
		r.CacheWritePer1K = value
	case "cache_creation_input_token_cost_above_1hr":
		r.CacheWrite1HPer1K = value
	}
}
