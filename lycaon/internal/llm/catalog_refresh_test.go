package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Refresh with empty live discovery must keep the eligible catalog.
func TestRefreshDiscoveryKeepsEligibleCatalog(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfgDir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	t.Cleanup(srv.Close)

	catalogPath := filepath.Join(t.TempDir(), "providers.yaml")
	testutil.FailErr(t, "write catalog", os.WriteFile(catalogPath, []byte(`
providers:
  - id: openai
    kind: openai
    label: OpenAI
    base_url: `+srv.URL+`/v1
    api_key_env: OPENAI_API_KEY
    models: []
    http_retry:
      max_retries: 1
      max_wait_ms: 1000
      backoff_ms: [1]
      statuses: [429]
      wait_headers: [Retry-After]
`), 0o600))

	localPath := filepath.Join(cfgDir, "providers.local.yaml")
	shipYAML, err := os.ReadFile(catalogPath)
	testutil.FailErr(t, "read ship", err)
	writeProvidersLocal(t, localPath, shipYAML)
	cat, err := NewProviderCatalog()
	testutil.FailErr(t, "catalog", err)
	creds, err := providercredentials.New()
	testutil.FailErr(t, "creds", err)
	testutil.FailErr(t, "set key", creds.Set("openai", "sk-test"))

	reg, err := NewRegistry(cat, creds)
	testutil.FailErr(t, "registry", err)
	reg.discoveryClient = srv.Client()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "modelfeed", "testdata", "models_dev_fixture.json"))
	testutil.FailErr(t, "fixture", err)
	feed, err := modelfeed.New(modelfeed.Options{
		CacheDir: filepath.Join(cfgDir, "modelfeed"),
		GetBytes: func(context.Context, string) ([]byte, error) { return raw, nil },
	})
	testutil.FailErr(t, "feed", err)
	_, err = feed.Refresh(context.Background())
	testutil.FailErr(t, "refresh feed", err)
	reg.SetModelFeed(feed)

	before := reg.EffectiveModels(t.Context(), "openai")
	if len(before) == 0 {
		t.Fatal("expected eligible catalog before refresh")
	}
	reg.RefreshDiscovery("openai")
	after := reg.EffectiveModels(t.Context(), "openai")
	if len(after) == 0 {
		t.Fatal("refresh must not wipe eligible catalog on empty discovery")
	}
	if len(after) != len(before) {
		t.Fatalf("eligible count changed after empty refresh: before=%d after=%d", len(before), len(after))
	}
	n, err := reg.TestConnectivity(t.Context(), "openai")
	testutil.FailErr(t, "TestConnectivity", err)
	if n != len(before) {
		t.Fatalf("TestConnectivity model count = %d, want %d (catalog kept)", n, len(before))
	}
}
