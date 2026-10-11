package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRegistryTestConnectivityAzureProbesDataPlaneKey(t *testing.T) {
	const validKey = "valid-azure-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("api-key") != validKey {
			http.Error(w, "invalid key", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	t.Cleanup(srv.Close)

	newRegistry := func(t *testing.T, key string) *Registry {
		t.Helper()
		entry := CatalogEntry{
			ID:             "azure",
			Kind:           "azure",
			BaseURL:        srv.URL,
			APIKeyEnv:      "TEST_AZURE_KEY",
			RequiresAPIKey: true,
			Models:         []modelinfo.Entry{{ID: "customer-deployment", PricedAs: "gpt-4o"}},
		}
		catalog := mustTestProviderCatalog(t, entry)
		credentials := providercredentials.NewAt(t.TempDir() + "/credential-vault.age")
		testutil.FailErr(t, "store Azure key", credentials.Set("azure", key))
		registry, err := NewRegistry(t.Context(), catalog, credentials)
		testutil.FailErr(t, "create registry", err)
		registry.discoveryClient = srv.Client()
		return registry
	}

	count, err := newRegistry(t, validKey).TestConnectivity(t.Context(), "azure")
	testutil.FailErr(t, "test valid Azure connection", err)
	if count != 1 {
		t.Fatalf("models = %d, want the configured deployment", count)
	}
	if _, err := newRegistry(t, "bogus").TestConnectivity(t.Context(), "azure"); err == nil {
		t.Fatal("bogus Azure key passed TestConnectivity")
	}
}
