package pricing

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFeedPreservesExplicitZeroAndMissingPrices(t *testing.T) {
	for _, parse := range []struct {
		name  string
		parse func([]byte) (RateTable, error)
		body  string
	}{
		{"models-dev", parseModelFeedFixture, `{"openai":{"models":{"free":{"cost":{"input":0,"output":0,"cache_read":0}},"partial":{"cost":{"input":1}}}}}`},
		{"litellm", ParseLitellm, `{"free":{"litellm_provider":"openai","input_cost_per_token":0,"output_cost_per_token":0,"cache_read_input_token_cost":0},"partial":{"litellm_provider":"openai","input_cost_per_token":1}}`},
	} {
		t.Run(parse.name, func(t *testing.T) {
			table, err := parse.parse([]byte(parse.body))
			testutil.FailErr(t, "parse rate presence", err)
			free := table.Rates[RateKey{Kind: "openai", ModelID: "free"}]
			partial := table.Rates[RateKey{Kind: "openai", ModelID: "partial"}]
			if !free.Valid() || free.CacheReadPer1K == nil || *free.CacheReadPer1K != 0 || free.CacheWritePer1K != nil {
				t.Fatalf("free rate = %+v", free)
			}
			if partial.InputPer1K == nil || partial.OutputPer1K != nil || partial.CacheReadPer1K != nil {
				t.Fatalf("partial rate = %+v", partial)
			}
		})
	}
}

func TestLiteLLMCacheLifetimeAndContextTiers(t *testing.T) {
	r, err := ParseLiteLLMRate([]byte(`{"input_cost_per_token":0.001,"output_cost_per_token":0.002,"cache_creation_input_token_cost_above_1hr":0.002,"input_cost_per_token_above_272k_tokens":0.003,"cache_creation_input_token_cost_above_1hr_above_272k_tokens":0.006}`))
	testutil.FailErr(t, "parse lifetime tiers", err)
	if *r.CacheWrite1HPer1K != 2 {
		t.Fatalf("base rate = %+v", r)
	}
	tier := r.ForPrompt(272001)
	if *tier.InputPer1K != 3 || *tier.CacheWrite1HPer1K != 6 || tier.CacheReadPer1K != nil {
		t.Fatalf("tier = %+v", tier)
	}
}

func TestFeedRetainsCacheWriteOnlyRate(t *testing.T) {
	table, err := ParseLitellm([]byte(`{"cache-only":{"litellm_provider":"openai","cache_creation_input_token_cost_above_1hr":0.002}}`))
	testutil.FailErr(t, "parse cache-write-only rate", err)
	rate, ok := table.Rates[RateKey{Kind: "openai", ModelID: "cache-only"}]
	if !ok || rate.CacheWrite1HPer1K == nil || *rate.CacheWrite1HPer1K != 2 || rate.InputPer1K != nil {
		t.Fatalf("cache-write-only rate = %+v, present=%v", rate, ok)
	}
}
