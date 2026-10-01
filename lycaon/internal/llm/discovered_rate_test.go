package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/internal/pricing"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestModelMetadataPreservesFreeAndMissingPrices(t *testing.T) {
	models := modelEntriesToAPI([]modelinfo.Entry{{ID: "free", DiscoveredPricing: &pricing.Rate{
		InputPer1K: new(float64(0)), Currency: "USD"}}}, "together", RoleExclusions{}, providerprofile.Default())
	raw, err := json.Marshal(models[0])
	testutil.FailErr(t, "marshal model metadata", err)
	var fields map[string]any
	testutil.FailErr(t, "decode model metadata", json.Unmarshal(raw, &fields))
	if value, ok := fields["input_per_1k_nano_usd"]; !ok || value != float64(0) {
		t.Fatalf("free input rate lost: %s", raw)
	}
	if _, ok := fields["output_per_1k_nano_usd"]; ok {
		t.Fatalf("missing output price invented: %s", raw)
	}
}

func TestLivePricingSurvivesModelMerges(t *testing.T) {
	const id = "Qwen/Qwen3.8-Flash"
	live := modelinfo.Entry{ID: id}
	rate := pricing.Rate{Currency: "USD", InputPer1K: new(float64(0.00015)), OutputPer1K: new(float64(0.00047)), CacheReadPer1K: new(float64(0))}
	modelinfo.ApplyDiscoveredRate(&live, rate)
	for name, entries := range map[string][]modelinfo.Entry{
		"local id":        {mergeDiscoveredModelEntry("together", []modelinfo.Entry{{ID: id}}, live)},
		"catalog prices":  {enrichFromDiscovery(modelinfo.Entry{ID: id, InputPer1K: 1, OutputPer1K: 2, PriceProvenance: modelinfo.PriceProvenanceCatalog}, live)},
		"local controls":  {overlayLocalModel(live, modelinfo.Entry{ID: id, InputPer1K: 1, MaxTokens: 100})},
		"catalog absent":  mergeAssignableModels("together", []modelinfo.Entry{{ID: id}}, []modelinfo.Entry{live}, nil, nil, "", false).Models,
		"catalog present": mergeAssignableModels("together", []modelinfo.Entry{{ID: id}}, []modelinfo.Entry{live}, nil, &modelfeed.Document{Providers: map[string]modelfeed.Provider{}}, modelfeed.StatusOK, true).Models,
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := modelinfo.DiscoveredRateFromEntries(entries, id)
			if !ok || !reflect.DeepEqual(got, rate) {
				t.Fatalf("live rate = %+v, present=%v", got, ok)
			}
			estimate := cost.ApplyRate(got, cost.TokenUsage{PromptTokens: 1000, CompletionTokens: 100, CacheReadInputTokens: 500})
			if estimate.Unpriced || estimate.EstimatedUSD < 0.0001219 || estimate.EstimatedUSD > 0.0001221 {
				t.Fatalf("estimate=%+v", estimate)
			}
		})
	}
}

func TestTogetherPaidCacheRateReachesCostLedger(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"glm","type":"chat","pricing":{"input":0.15,"output":0.5,"cached_input":0.03}}]`))
	}))
	t.Cleanup(server.Close)
	models, err := discovery.FromProfile(t.Context(), server.URL, "", server.Client(), discovery.TogetherProfile())
	testutil.FailErr(t, "discover Together cache rate", err)
	if len(models) != 1 {
		t.Fatalf("discovered models=%v", models)
	}
	live := models[0]
	entry := enrichFromDiscovery(modelinfo.Entry{ID: "glm"}, live)
	rate, ok := modelinfo.DiscoveredRateFromEntries([]modelinfo.Entry{entry}, "glm")
	if !ok || rate.CacheReadPer1K == nil || *rate.CacheReadPer1K != 0.00003 {
		t.Fatalf("cache rate=%+v", rate)
	}
	estimate := cost.ApplyRate(rate, cost.TokenUsage{PromptTokens: 1000, CacheReadInputTokens: 800, CompletionTokens: 100})
	if estimate.Unpriced || estimate.EstimatedUSD < 0.0001039 || estimate.EstimatedUSD > 0.0001041 {
		t.Fatalf("cached estimate=%+v", estimate)
	}
}
