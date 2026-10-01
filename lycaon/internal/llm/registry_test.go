package llm

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// mustCatalogCloneShipToLocal stages shipYAML as the ship catalog and clones it
// into local, the shape tests need when they want live instances rather than
// Add-picker templates.
func mustCatalogCloneShipToLocal(t *testing.T, shipYAML string) *ProviderCatalog {
	t.Helper()
	stageShipProviders(t, shipYAML)
	localPath := filepath.Join(t.TempDir(), "providers.local.yaml")
	if err := os.WriteFile(localPath, []byte(shipYAML), 0o644); err != nil {
		testutil.FailErr(t, "write providers.local", err)
	}
	catalog, err := NewProviderCatalogAt(localPath)
	testutil.FailErr(t, "NewProviderCatalogAt failed", err)
	return catalog
}

func TestRegistryKeyResolutionStoredOnly(t *testing.T) {
	tmp := t.TempDir()
	shipYAML := `providers:
  - id: test-provider
    base_url: https://example.com/v1
    api_key_env: TEST_LYCAON_API_KEY
    models:
      - id: model-a
` + MinimalShipHTTPRetryYAML

	credPath := filepath.Join(tmp, "credential-vault.age")
	creds := providercredentials.NewAt(credPath)

	catalog := mustCatalogCloneShipToLocal(t, shipYAML)
	registry, err := NewRegistry(catalog, creds)
	testutil.FailErr(t, "NewRegistry failed", err)

	t.Setenv("TEST_LYCAON_API_KEY", "env-key")
	if err := registry.Reload(t.Context()); err != nil {
		testutil.FailErr(t, "registry.Reload failed", err)
	}
	if registry.IsConfigured("test-provider") {
		t.Fatal("expected unconfigured: env alone must not configure")
	}
	entry, ok := catalog.Get("test-provider")
	if !ok {
		t.Fatal("catalog.Get(\"test-provider\") missing after reload")
	}
	if got := registry.resolveAPIKey(entry); got != "" {
		t.Fatalf("resolveAPIKey = %q, want empty (env ignored)", got)
	}

	if err := creds.Set("test-provider", "stored-key"); err != nil {
		testutil.FailErr(t, "creds.Set failed", err)
	}
	if err := registry.Reload(t.Context()); err != nil {
		testutil.FailErr(t, "registry.Reload failed", err)
	}
	if !registry.IsConfigured("test-provider") {
		t.Fatal("expected configured with stored credential")
	}
	if got := registry.resolveAPIKey(entry); got != "stored-key" {
		t.Fatalf("resolveAPIKey = %q, want stored-key", got)
	}
}

func TestRegistryOllamaConfiguredWithoutKey(t *testing.T) {
	tmp := t.TempDir()
	shipYAML := `providers:
  - id: ollama
    base_url: http://localhost:11434/v1
    api_key_env: ""
    models:
      - id: llama3.1
` + MinimalShipHTTPRetryYAML

	catalog := mustCatalogCloneShipToLocal(t, shipYAML)
	registry, err := NewRegistry(catalog, providercredentials.NewAt(filepath.Join(tmp, "credential-vault.age")))
	testutil.FailErr(t, "NewRegistry failed", err)
	if !registry.IsConfigured("ollama") {
		t.Fatal("ollama should be configured without api key")
	}
}

func TestRegistryProviderMutationInvalidatesAuthenticationObservationAtomically(t *testing.T) {
	shipYAML := `providers:
  - id: local
    kind: ollama
    base_url: http://localhost:11434/v1
    api_key_env: ""
    models: []
` + MinimalShipHTTPRetryYAML
	catalog := mustCatalogCloneShipToLocal(t, shipYAML)
	registry, err := NewRegistry(catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
	testutil.FailErr(t, "NewRegistry", err)
	registry.publishConnectivityResult(registry.snapshot.Load(), "local", nil)
	if _, ok := registry.snapshot.Load().observations["local"]; !ok {
		t.Fatal("authentication observation was not published")
	}
	testutil.FailErr(t, "reload provider mutation", registry.ReloadProviderMutation(t.Context(), "local"))
	if _, ok := registry.snapshot.Load().observations["local"]; ok {
		t.Fatal("provider mutation retained a stale authentication observation")
	}
}

func TestRegistryAnyConfigured(t *testing.T) {
	tmp := t.TempDir()
	shipYAML := `providers:
  - id: test-provider
    base_url: https://example.com/v1
    api_key_env: TEST_LYCAON_ANY_KEY
    models:
      - id: model-a
` + MinimalShipHTTPRetryYAML
	creds := providercredentials.NewAt(filepath.Join(tmp, "credential-vault.age"))
	catalog := mustCatalogCloneShipToLocal(t, shipYAML)
	registry, err := NewRegistry(catalog, creds)
	testutil.FailErr(t, "NewRegistry failed", err)
	t.Setenv("TEST_LYCAON_ANY_KEY", "secret")
	if registry.AnyConfigured() {
		t.Fatal("expected no configured providers from env alone")
	}
	if got := registry.ConfiguredCount(); got != 0 {
		t.Fatalf("ConfiguredCount = %d, want 0", got)
	}
	if err := creds.Set("test-provider", "secret"); err != nil {
		testutil.FailErr(t, "creds.Set failed", err)
	}
	if err := registry.Reload(t.Context()); err != nil {
		testutil.FailErr(t, "registry.Reload failed", err)
	}
	if !registry.AnyConfigured() {
		t.Fatal("expected configured provider after storing API key")
	}
	if got := registry.ConfiguredCount(); got != 1 {
		t.Fatalf("ConfiguredCount = %d, want 1", got)
	}
}

func TestRegistryKeepsMultipleProvidersForIndependentModelAssignments(t *testing.T) {
	tmp := t.TempDir()
	bundled := filepath.Join(tmp, "providers.yaml")
	data := `providers:
  - id: provider-a
    kind: openai-compatible
    base_url: https://a.example.com/v1
    api_key_env: PROVIDER_A_KEY
    models:
      - id: model-a
` + MinimalShipHTTPRetryYAML + `  - id: provider-b
    kind: openai-compatible
    base_url: https://b.example.com/v1
    api_key_env: PROVIDER_B_KEY
    models:
      - id: model-b
` + MinimalShipHTTPRetryYAML
	if err := os.WriteFile(bundled, []byte(data), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	catalog := mustCatalogCloneShipToLocal(t, data)
	registry, err := NewRegistry(catalog, providercredentials.NewAt(filepath.Join(tmp, "credential-vault.age")))
	testutil.FailErr(t, "NewRegistry", err)
	if got := registry.snapshot.Load().ids; len(got) != 2 || got[0] != "provider-a" || got[1] != "provider-b" {
		t.Fatalf("registered providers = %v, want both independent instances", got)
	}
	for _, ref := range []ModelRef{
		{ProviderID: "provider-a", Model: "model-a"},
		{ProviderID: "provider-b", Model: "model-b"},
	} {
		if _, err := registry.Get(ref.ProviderID); err != nil {
			t.Fatalf("model assignment %+v cannot resolve its provider: %v", ref, err)
		}
	}
}

func TestRegistryListNeverIncludesSecrets(t *testing.T) {
	tmp := t.TempDir()
	shipYAML := `providers:
  - id: test-provider
    base_url: https://example.com/v1
    api_key_env: TEST_LYCAON_LIST_KEY
    models:
      - id: model-a
` + MinimalShipHTTPRetryYAML

	creds := providercredentials.NewAt(filepath.Join(tmp, "credential-vault.age"))
	catalog := mustCatalogCloneShipToLocal(t, shipYAML)
	registry, err := NewRegistry(catalog, creds)
	testutil.FailErr(t, "NewRegistry failed", err)

	if err := creds.Set("test-provider", "super-secret-key"); err != nil {
		testutil.FailErr(t, "creds.Set failed", err)
	}
	if err := registry.Reload(t.Context()); err != nil {
		testutil.FailErr(t, "registry.Reload failed", err)
	}

	for _, meta := range registry.List(t.Context()) {
		if meta.BaseURL == "" {
			t.Fatalf("meta missing base_url: %+v", meta)
		}
	}
}

func TestRegistryListCacheIgnoresStaleRefreshAfterInvalidate(t *testing.T) {
	tmp := t.TempDir()
	shipYAML := `providers:
  - id: test-provider
    base_url: https://example.com/v1
    api_key_env: TEST_LYCAON_CACHE_KEY
    models:
      - id: model-a
` + MinimalShipHTTPRetryYAML
	creds := providercredentials.NewAt(filepath.Join(tmp, "credential-vault.age"))
	catalog := mustCatalogCloneShipToLocal(t, shipYAML)
	registry, err := NewRegistry(catalog, creds)
	testutil.FailErr(t, "NewRegistry failed", err)

	warm := registry.ListCached(t.Context())
	var unconfigured bool
	for _, p := range warm {
		if p.ID == "test-provider" && !p.Configured {
			unconfigured = true
		}
	}
	if !unconfigured {
		t.Fatal("expected test-provider unconfigured before credential")
	}

	_, _, _, staleGen := registry.listCache.Read()
	registry.listCache.Expire()

	_ = registry.ListCached(t.Context()) // schedules background refresh with staleGen

	if err := creds.Set("test-provider", "stored-key"); err != nil {
		testutil.FailErr(t, "creds.Set failed", err)
	}
	if err := registry.Reload(t.Context()); err != nil {
		testutil.FailErr(t, "registry.Reload failed", err)
	}
	fresh := registry.ListCached(t.Context())
	var configured bool
	for _, p := range fresh {
		if p.ID == "test-provider" && p.Configured && p.CredentialPresent {
			configured = true
		}
	}
	if !configured {
		t.Fatal("expected configured after credential + ListCached")
	}

	// Simulate a late stale refresh completing after invalidate/reload.
	registry.listCache.Store(warm, staleGen)

	again := registry.ListCached(t.Context())
	for _, p := range again {
		if p.ID == "test-provider" && !p.Configured {
			t.Fatal("stale list refresh must not overwrite post-credential cache")
		}
	}
}

// A credential rebuild invalidates an in-flight list read.
func TestRegistryListCacheReadDuringCredentialRebuildDoesNotPinStaleList(t *testing.T) {
	discoveryStarted := make(chan struct{})
	releaseDiscovery := make(chan struct{})
	var startedOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseDiscovery) }) }
	t.Cleanup(release)
	// Only the rebuild's discovery carries the stored key; it blocks until released.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			startedOnce.Do(func() { close(discoveryStarted) })
			<-releaseDiscovery
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a","object":"model"}]}`))
	}))
	t.Cleanup(backend.Close)

	shipYAML := `providers:
  - id: test-provider
    base_url: ` + backend.URL + `
    api_key_env: TEST_LYCAON_REBUILD_KEY
    models:
      - id: model-a
` + MinimalShipHTTPRetryYAML
	creds := providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age"))
	catalog := mustCatalogCloneShipToLocal(t, shipYAML)
	registry, err := NewRegistry(catalog, creds)
	testutil.FailErr(t, "NewRegistry failed", err)

	find := func(list []api.ProviderMeta) *api.ProviderMeta {
		for i := range list {
			if list[i].ID == "test-provider" {
				return &list[i]
			}
		}
		return nil
	}
	if before := find(registry.ListCached(t.Context())); before == nil || before.CredentialPresent {
		t.Fatalf("expected test-provider listed without a credential, got %+v", before)
	}

	testutil.FailErr(t, "creds.Set failed", creds.Set("test-provider", "stored-key"))
	reloadDone := make(chan error, 1)
	go func() { reloadDone <- registry.ReloadProviderMutation(t.Context(), "test-provider") }()
	select {
	case <-discoveryStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("credential rebuild never reached discovery")
	}
	_ = registry.ListCached(t.Context())
	release()
	testutil.FailErr(t, "reload provider mutation", <-reloadDone)

	after := find(registry.ListCached(t.Context()))
	if after == nil || !after.CredentialPresent || !after.Configured {
		t.Fatalf("list read during the rebuild pinned a stale entry: %+v", after)
	}
}

func TestProviderCatalogRemoveHardDeletes(t *testing.T) {
	tmp := t.TempDir()
	shipYAML := `providers:
  - id: fireworks
    base_url: https://api.fireworks.ai/inference/v1
    api_key_env: FIREWORKS_API_KEY
    models:
      - id: model-a
    http_retry:
      max_retries: 1
      max_wait_ms: 1000
      backoff_ms: [1]
      statuses: [429]
      wait_headers: [Retry-After]
`
	localPath := filepath.Join(tmp, "providers.local.yaml")
	stageShipProviders(t, shipYAML)
	catalog, err := NewProviderCatalogAt(localPath)
	testutil.FailErr(t, "NewProviderCatalogAt failed", err)
	if _, ok := catalog.Get("fireworks"); ok {
		t.Fatal("first-run local must stay empty — ship kinds are Add templates only")
	}
	if err := catalog.Put(ProviderEntry{
		ID:      "fireworks",
		Kind:    "fireworks",
		BaseURL: "https://api.fireworks.ai/inference/v1",
		Models:  []modelinfo.Entry{{ID: "model-a"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Reload(); err != nil {
		testutil.FailErr(t, "catalog.Reload failed", err)
	}
	entry, ok := catalog.Get("fireworks")
	if !ok || entry.BaseURL != "https://api.fireworks.ai/inference/v1" {
		t.Fatalf("put fireworks = %#v ok=%v", entry, ok)
	}
	if err := catalog.Put(ProviderEntry{
		ID:      "fireworks",
		Kind:    "fireworks",
		BaseURL: "http://127.0.0.1:60242",
		Models:  []modelinfo.Entry{{ID: "model-a"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Reload(); err != nil {
		testutil.FailErr(t, "catalog.Reload failed", err)
	}
	entry, _ = catalog.Get("fireworks")
	if entry.BaseURL != "http://127.0.0.1:60242" {
		t.Fatalf("updated base_url = %q", entry.BaseURL)
	}
	removed, err := catalog.Remove("fireworks")
	if err != nil || !removed {
		t.Fatalf("Remove fireworks: removed=%v err=%v", removed, err)
	}
	if err := catalog.Reload(); err != nil {
		testutil.FailErr(t, "catalog.Reload failed", err)
	}
	if _, ok := catalog.Get("fireworks"); ok {
		t.Fatal("after remove fireworks must not reappear from ship catalog")
	}
	removed, err = catalog.Remove("fireworks")
	if err != nil || removed {
		t.Fatalf("second Remove: removed=%v err=%v", removed, err)
	}
	kinds := catalog.KindTemplates()
	if len(kinds) != 1 || kinds[0].Kind != "fireworks" {
		t.Fatalf("KindTemplates = %#v", kinds)
	}
}

func TestProviderCatalogFirstRunLocalEmpty(t *testing.T) {
	tmp := t.TempDir()
	shipYAML := `providers:
  - id: openai
    base_url: https://api.openai.com/v1
    api_key_env: OPENAI_API_KEY
    models: []
` + MinimalShipHTTPRetryYAML
	local := filepath.Join(tmp, "providers.local.yaml")
	stageShipProviders(t, shipYAML)
	catalog, err := NewProviderCatalogAt(local)
	testutil.FailErr(t, "NewProviderCatalogAt", err)
	if got := catalog.List(); len(got) != 0 {
		t.Fatalf("first-run local List = %#v, want empty", got)
	}
	data, err := os.ReadFile(local)
	testutil.FailErr(t, "read local", err)
	if !strings.Contains(string(data), "providers:") {
		t.Fatalf("local file missing providers key: %s", data)
	}
}

func TestProviderCatalogPutUsesReplacementHTTPRetry(t *testing.T) {
	shipYAML := `providers:
  - id: hosted
    kind: hosted
    base_url: https://ship.example/v1
    api_key_env: HOSTED_KEY
    models: []
` + MinimalShipHTTPRetryYAML
	localYAML := `providers:
  - id: hosted-1
    kind: hosted
    label: Before
    base_url: https://local.example/v1
    api_key_env: HOSTED_KEY
    models: []
    http_retry:
      max_retries: 2
      max_wait_ms: 2000
      backoff_ms: [10, 20]
      statuses: [429, 503]
      wait_headers: [Retry-After]
`
	stageShipProviders(t, shipYAML)
	localPath := filepath.Join(t.TempDir(), "providers.local.yaml")
	writeProvidersLocal(t, localPath, []byte(localYAML))
	catalog, err := NewProviderCatalogAt(localPath)
	testutil.FailErr(t, "NewProviderCatalogAt", err)

	err = catalog.Put(ProviderEntry{
		ID: "hosted-1", Kind: "hosted", Label: "After",
		BaseURL: "https://local.example/v1", APIKeyEnv: "HOSTED_KEY", Models: []modelinfo.Entry{},
	})
	testutil.FailErr(t, "ProviderCatalog.Put", err)
	testutil.FailErr(t, "ProviderCatalog.Reload", catalog.Reload())
	entry, ok := catalog.Get("hosted-1")
	if !ok {
		t.Fatal("updated provider missing")
	}
	if entry.Label != "After" || entry.HTTPRetry.MaxRetries != 1 || len(entry.HTTPRetry.BackoffMs) != 1 {
		t.Fatalf("updated provider = %+v, want ship retry policy on full replacement", entry)
	}
	stored, err := os.ReadFile(localPath)
	testutil.FailErr(t, "read replaced local provider", err)
	if strings.Contains(string(stored), "http_retry:") {
		t.Fatalf("inherited retry policy was snapshotted into local config: %s", stored)
	}
}

func TestProviderCatalogPutRejectsKindChange(t *testing.T) {
	shipYAML := `providers:
  - id: hosted
    kind: hosted
    base_url: https://ship.example/v1
    api_key_env: HOSTED_KEY
    models: []
` + MinimalShipHTTPRetryYAML
	localYAML := `providers:
  - id: hosted-1
    kind: hosted
    base_url: https://local.example/v1
    api_key_env: HOSTED_KEY
    models: []
` + MinimalShipHTTPRetryYAML
	stageShipProviders(t, shipYAML)
	localPath := filepath.Join(t.TempDir(), "providers.local.yaml")
	writeProvidersLocal(t, localPath, []byte(localYAML))
	catalog, err := NewProviderCatalogAt(localPath)
	testutil.FailErr(t, "NewProviderCatalogAt", err)

	err = catalog.Put(ProviderEntry{
		ID: "hosted-1", Kind: "other", BaseURL: "https://other.example/v1",
		APIKeyEnv: "OTHER_KEY", Models: []modelinfo.Entry{},
	})
	if err == nil || !strings.Contains(err.Error(), "kind is immutable") {
		t.Fatalf("ProviderCatalog.Put kind change error = %v, want immutable rejection", err)
	}
	testutil.FailErr(t, "ProviderCatalog.Reload", catalog.Reload())
	entry, ok := catalog.Get("hosted-1")
	if !ok || entry.Kind != "hosted" || entry.BaseURL != "https://local.example/v1" {
		t.Fatalf("provider after rejected kind change = %+v, found %v", entry, ok)
	}
}

func TestRegistryListIncludesCatalogWhenSnapshotNil(t *testing.T) {
	shipYAML := `providers:
  - id: fireworks-1
    kind: fireworks
    base_url: https://api.fireworks.ai/inference/v1
    api_key_env: FIREWORKS_API_KEY
    models:
      - id: glm
` + MinimalShipHTTPRetryYAML
	catalog := mustCatalogCloneShipToLocal(t, shipYAML)
	registry, err := NewRegistry(catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
	testutil.FailErr(t, "NewRegistry", err)
	registry.snapshot.Store(nil)

	list := registry.List(t.Context())
	if len(list) != 1 || list[0].ID != "fireworks-1" {
		t.Fatalf("List with empty snapshot = %#v", list)
	}
	if list[0].Configured || list[0].ReadyToAssign {
		t.Fatalf("missing snapshot must be not ready: %+v", list[0])
	}
}

func TestRegistryListIncludesCatalogWhenSnapshotOmitsInstance(t *testing.T) {
	shipYAML := `providers:
  - id: fireworks-1
    kind: fireworks
    base_url: https://api.fireworks.ai/inference/v1
    api_key_env: FIREWORKS_API_KEY
    models:
      - id: glm
` + MinimalShipHTTPRetryYAML + `  - id: ollama-1
    kind: ollama
    base_url: http://localhost:11434/v1
    api_key_env: ""
    models:
      - id: gemma4
` + MinimalShipHTTPRetryYAML
	catalog := mustCatalogCloneShipToLocal(t, shipYAML)
	registry, err := NewRegistry(catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
	testutil.FailErr(t, "NewRegistry", err)
	current := registry.snapshot.Load()
	if current == nil {
		t.Fatal("expected snapshot after NewRegistry")
	}
	keep := current.providers["ollama-1"]
	if keep == nil {
		t.Fatal("expected ollama-1 in snapshot")
	}
	registry.snapshot.Store(newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Providers:        map[string]modelcall.Provider{"ollama-1": keep},
		Entries:          map[string]CatalogEntry{"ollama-1": current.entries["ollama-1"]},
		CredentialValues: map[string]string{"ollama-1": current.credentialValues["ollama-1"]},
		CredentialStored: map[string]bool{"ollama-1": current.credentialStored["ollama-1"]},
		Configured:       map[string]bool{"ollama-1": current.configured["ollama-1"]},
		Readiness:        map[string]providerReadiness{"ollama-1": current.readiness["ollama-1"]},
		DefaultID:        "ollama-1",
	}))

	list := registry.List(t.Context())
	found := map[string]bool{}
	for _, meta := range list {
		found[meta.ID] = true
		if meta.ID == "fireworks-1" && (meta.Configured || meta.ReadyToAssign) {
			t.Fatalf("omitted snapshot instance must be not ready: %+v", meta)
		}
	}
	if !found["fireworks-1"] || !found["ollama-1"] {
		t.Fatalf("List = %#v, want both catalog instances", list)
	}
}
