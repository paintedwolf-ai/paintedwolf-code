package llm

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRegistryCatalogAuthoritativeReadyToAssign(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfgDir)

	catalogPath := filepath.Join(t.TempDir(), "providers.yaml")
	testutil.FailErr(t, "write catalog", os.WriteFile(catalogPath, []byte(`
providers:
  - id: openai
    kind: openai
    label: OpenAI
    base_url: https://api.openai.com/v1
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
	testutil.FailErr(t, "refresh", err)
	reg.SetModelFeed(feed)

	// Empty discovery (no live /models) must keep eligible catalog.
	models := reg.EffectiveModels(t.Context(), "openai")
	if len(models) == 0 {
		t.Fatal("expected eligible catalog models with empty discovery")
	}
	for _, m := range models {
		if m.ID == "text-embedding-3-small" || m.ID == "gpt-image-1" {
			t.Fatalf("non-eligible id %q in assignable list", m.ID)
		}
	}

	list := reg.List(t.Context())
	if len(list) != 1 {
		t.Fatalf("list len = %d", len(list))
	}
	meta := list[0]
	if !meta.ReadyToAssign {
		t.Fatal("expected ready_to_assign")
	}
	if !meta.CatalogAuthoritative {
		t.Fatal("expected catalog_authoritative")
	}
	if meta.CatalogStatus != modelfeed.StatusOK {
		t.Fatalf("catalog_status = %q", meta.CatalogStatus)
	}
}
