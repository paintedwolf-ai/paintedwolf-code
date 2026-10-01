package discovery

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLiteLLMDiscoveryPreservesContextAndCacheRates(t *testing.T) {
	var record litellmModelInfoRecord
	testutil.FailErr(t, "decode tiered model info", json.Unmarshal([]byte(`{"model_name":"model","model_info":{"mode":"chat","input_cost_per_token":0.001,"output_cost_per_token":0.002,"cache_read_input_token_cost":0,"input_cost_per_token_above_200k_tokens":0.003,"cache_creation_input_token_cost_above_1hr_above_200k_tokens":0.006}}`), &record))
	entries := litellmChatCapableEntries([]litellmModelInfoRecord{record})
	rate, ok := modelinfo.DiscoveredRateFromEntries(entries, "model")
	if !ok || rate.CacheReadPer1K == nil || *rate.CacheReadPer1K != 0 {
		t.Fatalf("live rate = %+v", rate)
	}
	tier := rate.ForPrompt(200001)
	if tier.InputPer1K == nil || *tier.InputPer1K != 3 || tier.CacheWrite1HPer1K == nil || *tier.CacheWrite1HPer1K != 6 || tier.OutputPer1K != nil {
		t.Fatalf("tier = %+v", tier)
	}
}

func TestTogetherDiscoveryPreservesPricePresence(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		raw := `[{"id":"paid","type":"chat","pricing":{"input":0.15,"output":0.5,"cached_input":0}},{"id":"partial","type":"chat","pricing":{"input":0.15}},{"id":"missing","type":"chat"},{"id":"zero","type":"chat","pricing":{"input":0,"output":0}}]`
		if wrapped {
			raw = `{"data":` + raw + `}`
		}
		records, err := decodeDiscoveryCatalogArray(strings.NewReader(raw))
		testutil.FailErr(t, "decode catalog prices", err)
		paid := modelinfo.Entry{ID: "paid"}
		applyDiscoveryPricing(&paid, records[0], DiscoveryPricingPerMillion)
		rate, ok := modelinfo.DiscoveredRateFromEntries([]modelinfo.Entry{paid}, "paid")
		if !ok || rate.CacheReadPer1K == nil || *rate.CacheReadPer1K != 0 {
			t.Fatalf("free cache rate lost: %+v", rate)
		}
		partial := modelinfo.Entry{ID: "partial"}
		applyDiscoveryPricing(&partial, records[1], DiscoveryPricingPerMillion)
		if partial.DiscoveredPricing.OutputPer1K != nil || partial.DiscoveredPricing.CacheReadPer1K != nil {
			t.Fatal("missing rate was invented")
		}
		if discoveryCallability(records[3], TogetherProfile()).State != modelinfo.CapabilityUnsupported {
			t.Fatal("zero-price unavailable listing admitted")
		}
		zero := modelinfo.Entry{ID: "zero"}
		applyDiscoveryPricing(&zero, records[3], DiscoveryPricingPerMillion)
		if zero.DiscoveredPricing == nil || zero.DiscoveredPricing.InputPer1K == nil || *zero.DiscoveredPricing.InputPer1K != 0 {
			t.Fatal("explicit zero price lost")
		}
	}
	for _, raw := range []string{`[{"pricing":{"input":-1}}]`, `[{"pricing":{"input":0.1,"cached_input":-1}}]`} {
		if _, err := decodeDiscoveryCatalogArray(strings.NewReader(raw)); err == nil {
			t.Fatal("invalid rate accepted")
		}
	}
}
