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

func TestDiscoverFromProfileTogetherChatOnly(t *testing.T) {
	const wantAuth = "together_test_key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+wantAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id":   "Qwen/Qwen3.5-9B",
				"type": "chat",
				"pricing": map[string]float64{
					"input":  0.17,
					"output": 0.25,
				},
			},
			{
				// Unpriced chat rows remain visible but uncallable.
				"id":   "Qwen/Qwen3-32B",
				"type": "chat",
				"pricing": map[string]float64{
					"input":  0,
					"output": 0,
				},
			},
			{"id": "moonshotai/Kimi-K2.6", "type": "chat"},
			{"id": "black-forest-labs/FLUX.1-schnell", "type": "image"},
			{
				"id":   "codellama/CodeLlama-34b-Instruct",
				"type": "code",
				"pricing": map[string]float64{
					"input":  0.78,
					"output": 0.78,
				},
			},
		})
	}))
	defer srv.Close()

	models, err := discovery.FromProfile(
		context.Background(),
		srv.URL+"/v1",
		wantAuth,
		srv.Client(),
		discovery.TogetherProfile(),
	)
	testutil.FailErr(t, "discover together profile", err)

	// Wire type excludes non-chat rows.
	byID := make(map[string]modelinfo.Entry, len(models))
	for _, m := range models {
		byID[m.ID] = m
	}
	if len(models) != 3 {
		t.Fatalf("models = %v, want the 3 chat rows", modelIDs(models))
	}
	for _, unwanted := range []string{"black-forest-labs/FLUX.1-schnell", "codellama/CodeLlama-34b-Instruct"} {
		if _, present := byID[unwanted]; present {
			t.Fatalf("non-chat row %q must not be listed", unwanted)
		}
	}

	priced := byID["Qwen/Qwen3.5-9B"]
	if priced.InputPer1K != 0.00017 {
		t.Fatalf("pricing = %+v", priced)
	}
	if priced.Callable.State != modelinfo.CapabilitySupported {
		t.Fatalf("priced row callable = %q, want supported", priced.Callable.State)
	}
	for _, id := range []string{"Qwen/Qwen3-32B", "moonshotai/Kimi-K2.6"} {
		if got := byID[id].Callable.State; got != modelinfo.CapabilityUnsupported {
			t.Fatalf("unpriced row %q callable = %q, want unsupported", id, got)
		}
	}

	// Only callable rows are assignable.
	merged := mergeAssignableModels("together", nil, models, nil, nil, "", false)
	if len(merged.Models) != 1 || merged.Models[0].ID != "Qwen/Qwen3.5-9B" {
		t.Fatalf("assignable = %v, want [Qwen/Qwen3.5-9B]", modelIDs(merged.Models))
	}
	if len(merged.Refused) != 2 {
		t.Fatalf("refused = %v, want the 2 unpriced chat rows", modelIDs(merged.Refused))
	}
}

func TestDiscoverFromProfileOpenRouterTextOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.RawQuery; got != "output_modalities=text" {
			t.Fatalf("query = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id": "openai/gpt-4o",
					"pricing": map[string]string{
						"prompt":     "0.0000025",
						"completion": "0.00001",
					},
					"architecture": map[string]any{
						"output_modalities": []string{"text"},
					},
				},
				{
					"id": "black-forest-labs/flux",
					"architecture": map[string]any{
						"output_modalities": []string{"image"},
					},
				},
			},
		})
	}))
	defer srv.Close()

	models, err := discovery.FromProfile(
		context.Background(),
		srv.URL+"/v1",
		"key",
		srv.Client(),
		discovery.OpenRouterProfile(),
	)
	testutil.FailErr(t, "discover openrouter profile", err)
	if len(models) != 1 || models[0].ID != "openai/gpt-4o" {
		t.Fatalf("models = %+v", models)
	}
}

func TestRegistryTestConnectivityDiscoversModels(t *testing.T) {
	const wantAuth = "together-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "aaa/would-sort-first", "type": "chat", "pricing": map[string]float64{"input": 0.1, "output": 0.1}},
			{"id": "Qwen/Qwen3.5-9B", "type": "chat", "pricing": map[string]float64{"input": 0.17, "output": 0.25}},
		})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	user := tmp + "/providers.local.yaml"
	yaml := "providers:\n  - id: together\n    kind: together\n    base_url: " + srv.URL + "/v1\n    api_key_env: TEST_TOGETHER_KEY\n    models: []\n" + MinimalShipHTTPRetryYAML
	stageShipProviders(t, yaml)

	writeProvidersLocal(t, user, []byte(yaml))

	catalog, err := NewProviderCatalogAt(user)
	testutil.FailErr(t, "NewProviderCatalogAt failed", err)
	creds := providercredentials.NewAt(tmp + "/credential-vault.age")
	if err := creds.Set("together", wantAuth); err != nil {
		testutil.FailErr(t, "creds.Set failed", err)
	}
	registry, err := NewRegistry(t.Context(), catalog, creds)
	testutil.FailErr(t, "NewRegistry failed", err)
	registry.discoveryClient = srv.Client()

	n, err := registry.TestConnectivity(t.Context(), "together")
	testutil.FailErr(t, "TestConnectivity", err)
	if n != 2 {
		t.Fatalf("models = %d, want 2", n)
	}
}

func TestRegistryTestConnectivityFailsOnUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	yaml := "providers:\n  - id: together\n    kind: together\n    base_url: " + srv.URL + "/v1\n    api_key_env: TEST_TOGETHER_KEY\n    models: []\n" + MinimalShipHTTPRetryYAML
	stageShipProviders(t, yaml)
	localPath := tmp + "/providers.local.yaml"
	writeProvidersLocal(t, localPath, []byte(yaml))
	catalog, err := NewProviderCatalogAt(localPath)
	testutil.FailErr(t, "NewProviderCatalogAt failed", err)
	creds := providercredentials.NewAt(tmp + "/credential-vault.age")
	if err := creds.Set("together", "bad-key"); err != nil {
		testutil.FailErr(t, "creds.Set failed", err)
	}
	registry, err := NewRegistry(t.Context(), catalog, creds)
	testutil.FailErr(t, "NewRegistry failed", err)
	registry.discoveryClient = srv.Client()

	if _, err := registry.TestConnectivity(t.Context(), "together"); err == nil {
		t.Fatal("expected discovery failure")
	}
}

func TestRegistryListDiscoversConfiguredTogetherProvider(t *testing.T) {
	const wantAuth = "together-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "Qwen/Qwen3.5-9B", "type": "chat", "pricing": map[string]float64{"input": 0.17, "output": 0.25}},
			{"id": "moonshotai/Kimi-K2.6", "type": "chat", "pricing": map[string]float64{"input": 1.2, "output": 4.5}},
			{"id": "Qwen/Qwen3-32B", "type": "chat", "pricing": map[string]float64{"input": 0, "output": 0}},
			{"id": "black-forest-labs/FLUX.1-schnell", "type": "image"},
		})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	user := tmp + "/providers.local.yaml"
	yaml := "providers:\n  - id: together\n    kind: together\n    base_url: " + srv.URL + "/v1\n    api_key_env: TEST_TOGETHER_KEY\n    models:\n      - id: Qwen/Qwen2.5-Coder-32B-Instruct\n" + MinimalShipHTTPRetryYAML
	stageShipProviders(t, yaml)
	t.Setenv("TEST_TOGETHER_KEY", wantAuth)

	writeProvidersLocal(t, user, []byte(yaml))

	catalog, err := NewProviderCatalogAt(user)
	testutil.FailErr(t, "NewProviderCatalogAt failed", err)
	creds := providercredentials.NewAt(tmp + "/credential-vault.age")
	if err := creds.Set("together", wantAuth); err != nil {
		testutil.FailErr(t, "creds.Set failed", err)
	}
	registry, err := NewRegistry(t.Context(), catalog, creds)
	testutil.FailErr(t, "NewRegistry failed", err)
	registry.discoveryClient = srv.Client()

	var together *api.ProviderMeta
	for _, p := range registry.List(t.Context()) {
		if p.ID == "together" {
			cp := p
			together = &cp
			break
		}
	}
	if together == nil {
		t.Fatal("together missing from list")
	}
	if len(together.Models) != 2 {
		t.Fatalf("models = %+v, want chat-only list", together.Models)
	}
}
