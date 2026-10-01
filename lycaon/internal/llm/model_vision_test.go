package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRegistryModelHasVision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "gpt-4o"}},
		})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	// Local capability evidence applies to a discovered model with the same id.
	yaml := "providers:\n  - id: openai\n    base_url: " + srv.URL + "/v1\n    models:\n      - id: gpt-4o\n        capabilities:\n          vision:\n            state: supported\n            sources: [local-config]\n" + MinimalShipHTTPRetryYAML
	stageShipProviders(t, yaml)
	localPath := filepath.Join(tmp, "providers.local.yaml")
	writeProvidersLocal(t, localPath, []byte(yaml))
	catalog, err := NewProviderCatalogAt(localPath)
	testutil.FailErr(t, "catalog", err)
	reg, err := NewRegistry(catalog, providercredentials.NewAt(filepath.Join(tmp, "credential-vault.age")))
	testutil.FailErr(t, "registry", err)
	reg.discoveryClient = srv.Client()
	if !reg.ModelHasVision(t.Context(), "openai", "gpt-4o") {
		t.Fatal("expected gpt-4o vision")
	}
	if reg.ModelHasVision(t.Context(), "openai", "text-only") {
		t.Fatal("expected unknown model non-vision")
	}
}
