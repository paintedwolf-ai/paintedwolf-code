package llm

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/internal/pricing"
	"github.com/lycaon/lycaon/internal/testutil"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestCatalogAuthoritative(t *testing.T) {
	if !CatalogAuthoritative("openai", true) {
		t.Fatal("mapped + usable should be authoritative")
	}
	if CatalogAuthoritative("openai", false) {
		t.Fatal("mapped + unavailable must not be authoritative")
	}
	if CatalogAuthoritative("ollama", true) {
		t.Fatal("unmapped must not be authoritative")
	}
}

func TestFeedMissingCapabilityRemainsUnknown(t *testing.T) {
	doc := fixtureDoc(t)
	model := doc.Providers["openai"].Models["gpt-4.1"]
	model.ToolCall = nil
	model.Reasoning = nil
	caps := modelEntryFromFeed(model).Capabilities
	if caps.Tools.State != modelinfo.CapabilityUnknown || caps.Reasoning.State != modelinfo.CapabilityUnknown {
		t.Fatalf("missing fields became negative evidence: %+v", caps)
	}
	model.ToolCall = new(false)
	if modelEntryFromFeed(model).Capabilities.Tools.State != modelinfo.CapabilityUnsupported {
		t.Fatal("explicit negative evidence was lost")
	}
}

func TestDiscoveryKeepSetCoversKnownStrategies(t *testing.T) {
	want := []providerprofile.DiscoveryStrategy{
		providerprofile.DiscoveryConfigured,
		providerprofile.DiscoveryNone,
		providerprofile.DiscoveryOllama,
		providerprofile.DiscoveryLMStudio,
		providerprofile.DiscoveryOMLX,
		providerprofile.DiscoveryLiteLLM,
		providerprofile.DiscoveryGemini,
		providerprofile.DiscoveryVertexExpress,
		providerprofile.DiscoveryFireworks,
		providerprofile.DiscoveryCloudflareWorkersAI,
		providerprofile.DiscoveryAnthropic,
		providerprofile.DiscoveryAzure,
		providerprofile.DiscoveryBedrock,
		providerprofile.DiscoveryVertex,
	}
	for _, s := range want {
		gap, ok := DiscoveryKeepSetGap(s)
		if !ok || gap == "" {
			t.Fatalf("keep-set missing %q", s)
		}
	}
	if len(DiscoveryKeepSetSnapshot()) != len(want) {
		t.Fatalf("discoveryKeepSet size = %d, want %d", len(DiscoveryKeepSetSnapshot()), len(want))
	}
}

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

// Each live listing sends exactly one credential style.
func TestDiscoveryAuthContracts(t *testing.T) {
	const wantKey = "discovery-secret"

	t.Run("openai_compat_bearer_only", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+wantKey {
				http.Error(w, "want bearer", http.StatusUnauthorized)
				return
			}
			if r.Header.Get("x-api-key") != "" || r.Header.Get("api-key") != "" || r.Header.Get("x-goog-api-key") != "" {
				t.Fatalf("openai-compat must not send vendor key headers: %v", r.Header)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m1"}}})
		}))
		t.Cleanup(srv.Close)
		ids, err := discovery.OpenAIModels(context.Background(), srv.URL, wantKey, srv.Client())
		testutil.FailErr(t, "DiscoverOpenAIModels", err)
		if len(ids) != 1 || ids[0] != "m1" {
			t.Fatalf("ids = %v", ids)
		}
	})

	t.Run("gemini_goog_key_no_bearer", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("x-goog-api-key") != wantKey {
				http.Error(w, "want goog key", http.StatusUnauthorized)
				return
			}
			if auth := r.Header.Get("Authorization"); auth != "" {
				t.Fatalf("gemini native must not send Authorization (got %q)", auth)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{{
					"name": "models/gemini-2.5-flash",
					"supportedGenerationMethods": []string{
						"generateContent", "createCachedContent",
					},
				}},
			})
		}))
		t.Cleanup(srv.Close)
		models, err := discovery.GeminiModels(context.Background(), srv.URL+"/v1beta/openai", wantKey, srv.Client())
		testutil.FailErr(t, "DiscoverGeminiModels", err)
		if len(models) != 1 || models[0].ID != "gemini-2.5-flash" {
			t.Fatalf("models = %+v", models)
		}
	})

	t.Run("anthropic_x_api_key_no_bearer", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("x-api-key") != wantKey {
				http.Error(w, "want x-api-key", http.StatusUnauthorized)
				return
			}
			if auth := r.Header.Get("Authorization"); auth != "" {
				t.Fatalf("anthropic discovery must not send Authorization (got %q)", auth)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"id": "claude-sonnet-4-6"}},
			})
		}))
		t.Cleanup(srv.Close)
		models, err := discovery.AnthropicModels(context.Background(), srv.URL+"/v1", wantKey, srv.Client())
		testutil.FailErr(t, "DiscoverAnthropicModels", err)
		if len(models) != 1 || models[0].ID != "claude-sonnet-4-6" {
			t.Fatalf("models = %+v", models)
		}
	})

	t.Run("azure_arm_bearer_only_never_data_plane_api_key", func(t *testing.T) {
		// Azure discovery reads Resource Manager with an AAD bearer token. It
		// never sends the data-plane api-key passed as wantKey and never calls
		// the data-plane host.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "arm-token"})
			case strings.Contains(r.URL.Path, "/deployments"):
				if r.Header.Get("api-key") != "" {
					t.Fatalf("ARM must not receive the data-plane api-key header")
				}
				if r.Header.Get("Authorization") != "Bearer arm-token" {
					http.Error(w, "want arm bearer", http.StatusUnauthorized)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"value": []map[string]string{{"name": "gpt-4o-deploy"}},
				})
			default:
				t.Fatalf("unexpected ARM request path %q", r.URL.Path)
			}
		}))
		t.Cleanup(srv.Close)

		t.Setenv("AZURE_SUBSCRIPTION_ID", "sub-1")
		t.Setenv("AZURE_RESOURCE_GROUP", "rg-1")
		t.Setenv("AZURE_TENANT_ID", "tenant-1")
		t.Setenv("AZURE_CLIENT_ID", "client-1")
		t.Setenv("AZURE_CLIENT_SECRET", "secret-1")
		t.Setenv("AZURE_AUTHORITY_HOST", srv.URL)
		restoreARMHost := discovery.AzureManagementHost
		discovery.AzureManagementHost = srv.URL
		t.Cleanup(func() { discovery.AzureManagementHost = restoreARMHost })

		models, err := discovery.AzureModels(context.Background(), "https://acme.openai.azure.com", srv.Client())
		testutil.FailErr(t, "DiscoverAzureModels", err)
		if len(models) != 1 || models[0].ID != "gpt-4o-deploy" {
			t.Fatalf("models = %+v", models)
		}
	})
}
