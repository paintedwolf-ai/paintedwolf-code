package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDiscoverOpenRouterModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("output_modalities") != "text" {
			http.Error(w, "missing filter", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id":                   "openai/gpt-4o",
					"context_length":       128000,
					"supported_parameters": []string{"tools", "structured_outputs", "reasoning"},
					"pricing": map[string]string{
						"prompt":     "0.0000025",
						"completion": "0.00001",
					},
					"architecture": map[string]any{
						"input_modalities":  []string{"text", "image"},
						"output_modalities": []string{"text"},
					},
				},
				{
					"id": "stability/sdxl",
					"architecture": map[string]any{
						"output_modalities": []string{"image"},
					},
				},
			},
		})
	}))
	defer srv.Close()

	models, err := discovery.FromProfile(context.Background(), srv.URL+"/v1", "key", srv.Client(), discovery.OpenRouterProfile())
	testutil.FailErr(t, "DiscoverFromProfile openrouter failed", err)
	if len(models) != 1 {
		t.Fatalf("models = %+v", models)
	}
	if models[0].ID != "openai/gpt-4o" {
		t.Fatalf("id = %q", models[0].ID)
	}
	if models[0].ContextLength != 128000 {
		t.Fatalf("context_length = %d", models[0].ContextLength)
	}
	if models[0].InputPer1K != 0.0025 || models[0].OutputPer1K != 0.01 {
		t.Fatalf("pricing = %+v", models[0])
	}
	if models[0].Currency != "USD" {
		t.Fatalf("currency = %q", models[0].Currency)
	}
	caps := models[0].EffectiveCapabilities()
	if !modelinfo.Supported(caps.Tools) || !modelinfo.Supported(caps.StructuredOutput) ||
		!modelinfo.Supported(caps.Reasoning) || !modelinfo.Supported(caps.Vision) {
		t.Fatalf("capabilities = %+v", caps)
	}
}

func TestOpenRouterAbsentCapabilityMetadataRemainsUnknown(t *testing.T) {
	if got := discovery.ListAnyEvidence(nil, "tools"); got != modelinfo.CapabilityUnknown {
		t.Fatalf("nil supported_parameters = %q, want unknown", got)
	}
	if got := discovery.ListAnyEvidence([]string{}, "tools"); got != modelinfo.CapabilityUnsupported {
		t.Fatalf("empty supported_parameters = %q, want unsupported", got)
	}
}

func TestRegistryListDiscoversConfiguredOpenRouterProvider(t *testing.T) {
	const wantAuth = "openrouter-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer "+wantAuth {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{
						"id":             "anthropic/claude-sonnet-4",
						"context_length": 200000,
						"architecture": map[string]any{
							"input_modalities":  []string{"text"},
							"output_modalities": []string{"text"},
						},
						"pricing": map[string]string{
							"prompt":     "0.000003",
							"completion": "0.000015",
						},
					},
				},
			})
		case "/v1/chat/completions":
			if r.Header.Get("HTTP-Referer") != "https://paintedwolf.ai/" {
				http.Error(w, "missing referer", http.StatusBadRequest)
				return
			}
			if r.Header.Get("X-OpenRouter-Title") != "Painted Wolf Code" {
				http.Error(w, "missing title", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmp := t.TempDir()
	user := tmp + "/providers.local.yaml"
	yaml := "providers:\n  - id: openrouter\n    kind: openrouter\n    base_url: " + srv.URL + "/v1\n    api_key_env: TEST_OPENROUTER_KEY\n    models: []\n" + MinimalShipHTTPRetryYAML
	stageShipProviders(t, yaml)
	t.Setenv("TEST_OPENROUTER_KEY", wantAuth)

	writeProvidersLocal(t, user, []byte(yaml))

	catalog, err := NewProviderCatalogAt(user)
	testutil.FailErr(t, "NewProviderCatalogAt failed", err)
	creds := providercredentials.NewAt(tmp + "/credential-vault.age")
	if err := creds.Set("openrouter", wantAuth); err != nil {
		testutil.FailErr(t, "creds.Set failed", err)
	}
	registry, err := NewRegistry(catalog, creds)
	testutil.FailErr(t, "NewRegistry failed", err)
	registry.discoveryClient = srv.Client()

	var openrouter *api.ProviderMeta
	for _, p := range registry.List(t.Context()) {
		if p.ID == "openrouter" {
			cp := p
			openrouter = &cp
			break
		}
	}
	if openrouter == nil {
		t.Fatal("openrouter missing from list")
	}
	if len(openrouter.Models) != 1 {
		t.Fatalf("models = %+v", openrouter.Models)
	}
	if openrouter.Models[0].InputPer1KNanoUSD == nil || *openrouter.Models[0].InputPer1KNanoUSD != 3_000_000 || openrouter.Models[0].OutputPer1KNanoUSD == nil || *openrouter.Models[0].OutputPer1KNanoUSD != 15_000_000 {
		t.Fatalf("pricing = %+v", openrouter.Models[0])
	}

	// Get returns the driver behind the host's decorators, so the call goes
	// through the interface.
	p, err := registry.Get("openrouter")
	testutil.FailErr(t, "Get openrouter", err)
	_, err = p.Complete(context.Background(), modelcall.CompletionRequest{
		Model:    "anthropic/claude-sonnet-4",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	testutil.FailErr(t, "Complete", err)
}
