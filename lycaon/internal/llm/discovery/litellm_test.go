package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDiscoverLiteLLMModelsChatModeOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/model/info" {
			http.NotFound(w, r)
			return
		}
		in := 0.000003
		out := 0.000015
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"model_name": "gpt-4o",
					"model_info": map[string]any{
						"mode":                      "chat",
						"input_cost_per_token":      in,
						"output_cost_per_token":     out,
						"max_input_tokens":          128000,
						"supports_vision":           true,
						"supports_function_calling": true,
						"supports_prompt_caching":   true,
					},
				},
				{
					// No chat/completions support.
					"model_name": "text-embedding-3-small",
					"model_info": map[string]any{"mode": "embedding"},
				},
			},
		})
	}))
	defer srv.Close()

	models, err := LiteLLMModels(context.Background(), srv.URL+"/v1", "", srv.Client())
	testutil.FailErr(t, "discover litellm models", err)
	if len(models) != 1 {
		t.Fatalf("models = %+v, want 1 chat-mode entry (drop embedding)", models)
	}
	if models[0].ID != "gpt-4o" {
		t.Fatalf("model = %+v", models[0])
	}
	if !floatNear(models[0].InputPer1K, 0.003) || !floatNear(models[0].OutputPer1K, 0.015) {
		t.Fatalf("live pricing = %+v", models[0])
	}
	if models[0].PriceProvenance != modelinfo.PriceProvenanceDiscovered {
		t.Fatalf("provenance = %q", models[0].PriceProvenance)
	}
	if !modelinfo.Supported(models[0].Capabilities.Vision) || models[0].ContextLength != 128000 {
		t.Fatalf("vision/context = %+v", models[0])
	}
	caps := models[0].EffectiveCapabilities()
	if !modelinfo.Supported(caps.Tools) || caps.Reasoning.State != modelinfo.CapabilityUnknown {
		t.Fatalf("capabilities = %+v", caps)
	}
	// The proxy's per-model caching fact is what the catalog marker grammar reads.
	if !modelinfo.Supported(caps.PromptCaching) || caps.PromptCaching.Sources[0] != "litellm" {
		t.Fatalf("prompt caching = %+v, want supported from litellm", caps.PromptCaching)
	}
}

func TestDiscoverLiteLLMModelsFallsBackOn404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/model/info":
			http.NotFound(w, r)
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "gpt-4o"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	models, err := LiteLLMModels(context.Background(), srv.URL+"/v1", "", srv.Client())
	testutil.FailErr(t, "discover litellm models via fallback", err)
	if len(models) != 1 || models[0].ID != "gpt-4o" {
		t.Fatalf("models = %+v, want OpenAI-compat fallback list", models)
	}
	if !models[0].Untyped {
		t.Fatal("404 /models fallback must mark rows Untyped for fail-closed merge")
	}
}

func TestDiscoverLiteLLMModelsReturnsErrorWhenBothFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down for maintenance", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := LiteLLMModels(context.Background(), srv.URL+"/v1", "", srv.Client())
	if err == nil {
		t.Fatal("expected error when model/info fails with non-404 status")
	}
}
