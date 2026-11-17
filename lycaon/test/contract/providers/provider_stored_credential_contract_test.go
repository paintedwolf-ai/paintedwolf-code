package contract

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Every key-requiring bundled provider is configured by a stored credential
// alone: its key environment variable is ignored. The live row is seeded, so
// the credential logic decides rather than catalog membership.
func TestKeyRequiringBundledProvidersNeedStoredCredential(t *testing.T) {
	// Discovery after configuration reaches this server, never a hosted endpoint.
	discovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(discovery.Close)

	cfg, err := llm.LoadProviderConfig()
	contractcheck.FailErr(t, "llm.LoadProviderConfig failed", err)
	var rows []llm.ProviderEntry
	for _, p := range cfg.Providers {
		if p.RequiresKey() && p.AmbientAuth == "" {
			rows = append(rows, p)
		}
	}
	selected := map[string]bool{}
	for _, p := range rows {
		selected[p.ID] = true
	}
	for _, id := range []string{"anthropic", "cloudflare-workers-ai", "gemini", "mistral", "openai", "openrouter", "scaleway"} {
		if !selected[id] {
			t.Fatalf("key-requiring selection %v lacks %q", selected, id)
		}
	}

	for _, entry := range rows {
		t.Run(entry.ID, func(t *testing.T) {
			entry.BaseURL = discovery.URL + "/v1"
			if entry.Kind == "cloudflare-workers-ai" {
				entry.BaseURL = discovery.URL + "/client/v4/accounts/0123456789abcdef/ai/v1"
			}
			t.Setenv(entry.APIKeyEnv, "env-key-ignored")
			local := filepath.Join(t.TempDir(), "providers.local.yaml")
			writeLiveProvidersLocal(t, local, entry)
			catalog, err := llm.NewProviderCatalogAt(local)
			contractcheck.FailErr(t, "llm.NewProviderCatalogAt failed", err)

			creds := providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age"))
			if configuredWith(t, catalog, creds, entry.ID) {
				t.Fatalf("%s configured from its environment variable without a stored credential", entry.ID)
			}
			contractcheck.FailErr(t, "creds.Set failed", creds.Set(entry.ID, "stored-key"))
			if !configuredWith(t, catalog, creds, entry.ID) {
				t.Fatalf("%s not configured with a stored credential", entry.ID)
			}
		})
	}
}

func configuredWith(t *testing.T, catalog *llm.ProviderCatalog, creds *providercredentials.Store, id string) bool {
	t.Helper()
	registry, err := llm.NewRegistry(t.Context(), catalog, creds, nil)
	contractcheck.FailErr(t, "llm.NewRegistry failed", err)
	registry.SetOutboundSecretScreen(llm.NewModelSecretScreen(nil, nil))
	return registry.IsConfigured(id)
}
