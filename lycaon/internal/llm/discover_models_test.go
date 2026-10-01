package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestModelIDEquivalent(t *testing.T) {
	if !modelinfo.EquivalentID("ollama", "llama3.1", "llama3.1:latest") {
		t.Fatal("expected base name equivalence")
	}
	for _, pair := range [][2]string{
		{"gpt-4o", "gpt-4o-mini"},
		{"llama3.1:q4_K_M", "llama3.1:q8_0"},
		{
			"arn:aws:bedrock:us-east-1:111:application-inference-profile/custom-1",
			"arn:aws:bedrock:us-east-1:111:application-inference-profile/custom-2",
		},
	} {
		if modelinfo.EquivalentID("ollama", pair[0], pair[1]) {
			t.Fatalf("distinct model ids compare equal: %q and %q", pair[0], pair[1])
		}
	}
	if modelinfo.EquivalentID("bedrock", "model", "model:latest") {
		t.Fatal("non-Ollama model ids must remain opaque")
	}
}

func TestApplyDiscoveredModels(t *testing.T) {
	merged := applyDiscoveredModels(
		"ollama",
		[]modelinfo.Entry{{ID: "llama3.1", InputPer1K: 0.01, OutputPer1K: 0.02, Currency: "USD"}},
		[]modelinfo.Entry{{ID: "llama3.1:latest"}, {ID: "nomic-embed-text:latest"}},
	)
	if len(merged) != 2 {
		t.Fatalf("merged = %d entries, want 2", len(merged))
	}
	if merged[0].ID != "llama3.1:latest" || merged[0].InputPer1K != 0.01 {
		t.Fatalf("expected priced llama3.1:latest, got %+v", merged[0])
	}
	if merged[1].ID != "nomic-embed-text:latest" {
		t.Fatalf("expected nomic, got %+v", merged[1])
	}

	empty := applyDiscoveredModels(
		"openai",
		[]modelinfo.Entry{{ID: "gpt-4o"}},
		nil,
	)
	if len(empty) != 0 {
		t.Fatalf("empty discovery must not fall back to catalog: %+v", empty)
	}
}

func TestDiscoverOpenAIModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "llama3.1:latest"},
				{"id": "mistral:latest"},
			},
		})
	}))
	defer srv.Close()

	ids, err := discovery.OpenAIModels(context.Background(), srv.URL+"/v1", "", srv.Client())
	testutil.FailErr(t, "DiscoverOpenAIModels failed", err)
	if len(ids) != 2 || ids[0] != "llama3.1:latest" {
		t.Fatalf("ids = %v", ids)
	}
}

// Id-only model rows remain untyped during assignable merge.
func TestDiscoverFromProfileOpenAIMarksUntypedList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "gpt-4o"},
				{"id": "text-embedding-3-small"},
				{"id": "dall-e-3"},
			},
		})
	}))
	defer srv.Close()

	models, err := discovery.FromProfile(
		context.Background(),
		srv.URL+"/v1",
		"key",
		srv.Client(),
		discovery.OpenAIProfile(),
	)
	testutil.FailErr(t, "DiscoverFromProfile openai untyped", err)
	if len(models) != 3 {
		t.Fatalf("models = %+v, want 3 raw ids (merge filters assignable)", models)
	}
	for _, m := range models {
		if !m.Untyped {
			t.Fatalf("expected Untyped on %q", m.ID)
		}
	}
}

func TestRegistryListDiscoversLocalProviderModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{"name": "llama3.1:latest"}}})
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "tools"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmp := t.TempDir()
	user := tmp + "/providers.local.yaml"
	yaml := "providers:\n  - id: ollama\n    base_url: " + srv.URL + "/v1\n    api_key_env: \"\"\n    models: []\n" + MinimalShipHTTPRetryYAML
	stageShipProviders(t, yaml)
	writeProvidersLocal(t, user, []byte(yaml))
	catalog, err := NewProviderCatalogAt(user)
	testutil.FailErr(t, "NewProviderCatalogAt failed", err)
	registry, err := NewRegistry(catalog, providercredentials.NewAt(tmp+"/credential-vault.age"))
	testutil.FailErr(t, "NewRegistry failed", err)
	registry.discoveryClient = srv.Client()

	var ollama *api.ProviderMeta
	for _, p := range registry.List(t.Context()) {
		if p.ID == "ollama" {
			cp := p
			ollama = &cp
			break
		}
	}
	if ollama == nil {
		t.Fatal("ollama missing from list")
	}
	if len(ollama.Models) != 1 || ollama.Models[0].ID != "llama3.1:latest" {
		t.Fatalf("models = %+v", ollama.Models)
	}
	if !registry.ModelExists("ollama", "llama3.1") {
		t.Fatal("expected llama3.1 to match discovered llama3.1:latest")
	}
}

func TestRegistryListUntypedCompatRequiresAllowlist(t *testing.T) {
	const wantAuth = "secret-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+wantAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "gpt-4o"},
				{"id": "gpt-4o-mini"},
				{"id": "text-embedding-3-small"},
			},
		})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	user := tmp + "/providers.local.yaml"
	// Untyped id-only list plus unmapped kind is empty unless local models allowlist it.
	yaml := "providers:\n  - id: openai-compatible\n    kind: openai-compatible\n    base_url: " + srv.URL + "/v1\n    api_key_env: TEST_OPENAI_KEY\n    models:\n      - id: gpt-4o\n" + MinimalShipHTTPRetryYAML
	stageShipProviders(t, yaml)
	t.Setenv("TEST_OPENAI_KEY", wantAuth)

	writeProvidersLocal(t, user, []byte(yaml))

	catalog, err := NewProviderCatalogAt(user)
	testutil.FailErr(t, "NewProviderCatalogAt failed", err)
	creds := providercredentials.NewAt(tmp + "/credential-vault.age")
	if err := creds.Set("openai-compatible", wantAuth); err != nil {
		testutil.FailErr(t, "creds.Set failed", err)
	}
	registry, err := NewRegistry(catalog, creds)
	testutil.FailErr(t, "NewRegistry failed", err)
	registry.discoveryClient = srv.Client()

	var openai *api.ProviderMeta
	for _, p := range registry.List(t.Context()) {
		if p.ID == "openai-compatible" {
			cp := p
			openai = &cp
			break
		}
	}
	if openai == nil {
		t.Fatal("openai-compatible missing from list")
	}
	if !openai.Configured {
		t.Fatal("expected configured provider")
	}
	if len(openai.Models) != 1 || openai.Models[0].ID != "gpt-4o" {
		t.Fatalf("models = %+v, want allowlist∩live only (no embed, no gpt-4o-mini)", openai.Models)
	}
}

func TestRegistryListDiscoversConfiguredGeminiProvider(t *testing.T) {
	const wantAuth = "gemini-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("x-goog-api-key") != wantAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Fatalf("gemini native discovery must not send Authorization (got %q)", auth)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{
					"name": "models/gemini-3.5-flash",
					"supportedGenerationMethods": []string{
						"generateContent", "createCachedContent", "countTokens",
					},
				},
				{
					"name": "models/gemini-3.1-pro-preview",
					"supportedGenerationMethods": []string{
						"generateContent", "createCachedContent", "countTokens",
					},
				},
				{
					// Interactions-only agent: generateContent without cache.
					"name":                       "models/antigravity-preview-05-2026",
					"supportedGenerationMethods": []string{"generateContent", "countTokens"},
				},
			},
		})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	user := tmp + "/providers.local.yaml"
	yaml := "providers:\n  - id: gemini\n    kind: gemini\n    base_url: " + srv.URL + "/v1beta/openai\n    api_key_env: TEST_GEMINI_KEY\n    models:\n      - id: gemini-2.5-flash\n        input_per_1k: 0.0003\n        output_per_1k: 0.0025\n        currency: USD\n" + MinimalShipHTTPRetryYAML
	stageShipProviders(t, yaml)
	t.Setenv("TEST_GEMINI_KEY", wantAuth)

	writeProvidersLocal(t, user, []byte(yaml))

	catalog, err := NewProviderCatalogAt(user)
	testutil.FailErr(t, "NewProviderCatalogAt failed", err)
	creds := providercredentials.NewAt(tmp + "/credential-vault.age")
	if err := creds.Set("gemini", wantAuth); err != nil {
		testutil.FailErr(t, "creds.Set failed", err)
	}
	registry, err := NewRegistry(catalog, creds)
	testutil.FailErr(t, "NewRegistry failed", err)
	registry.discoveryClient = srv.Client()

	var gemini *api.ProviderMeta
	for _, p := range registry.List(t.Context()) {
		if p.ID == "gemini" {
			cp := p
			gemini = &cp
			break
		}
	}
	if gemini == nil {
		t.Fatal("gemini missing from list")
	}
	if !gemini.Configured {
		t.Fatal("expected configured provider")
	}
	if len(gemini.Models) != 2 {
		t.Fatalf("models = %+v, want live list without stale-model", gemini.Models)
	}
	if gemini.Models[0].ID != "gemini-3.1-pro-preview" || gemini.Models[1].ID != "gemini-3.5-flash" {
		t.Fatalf("models = %+v", gemini.Models)
	}
	if !registry.ModelExists("gemini", "gemini-3.5-flash") {
		t.Fatal("expected gemini-3.5-flash in effective models")
	}
}
