package llm

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/internal/testutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
)

func TestMergeBedrockInferenceProfilesExposesOnlyCallableProfileIDs(t *testing.T) {
	feed := []modelinfo.Entry{
		{ID: "anthropic.claude-3-5-sonnet", MaxTokens: 8192, Capabilities: modelinfo.ModelCapabilities{
			Chat:  modelinfo.Evidence(modelinfo.CapabilitySupported, "models.dev"),
			Tools: modelinfo.Evidence(modelinfo.CapabilitySupported, "models.dev"),
		}},
		{ID: "unavailable.foundation-model", Capabilities: modelinfo.ModelCapabilities{
			Chat: modelinfo.Evidence(modelinfo.CapabilitySupported, "models.dev"),
		}},
	}
	discovered := []modelinfo.Entry{{
		ID: "us.anthropic.claude-3-5-sonnet", PricedAs: "anthropic.claude-3-5-sonnet",
	}}
	got := mergeBedrockInferenceProfiles(nil, discovered, feed)
	if len(got) != 1 || got[0].ID != "us.anthropic.claude-3-5-sonnet" {
		t.Fatalf("models = %+v", got)
	}
	if got[0].PricedAs != "anthropic.claude-3-5-sonnet" ||
		!modelinfo.Supported(got[0].EffectiveCapabilities().Tools) {
		t.Fatalf("profile metadata = %+v", got[0])
	}
	if got[0].MaxTokens != 8192 {
		t.Fatalf("MaxTokens = %d, want the routed foundation model's output ceiling", got[0].MaxTokens)
	}
}

func TestMergeBedrockInferenceProfilesDropsUnverifiedFoundationModel(t *testing.T) {
	got := mergeBedrockInferenceProfiles(nil, []modelinfo.Entry{{
		ID: "custom-profile", PricedAs: "unknown.foundation-model",
	}}, nil)
	if len(got) != 0 {
		t.Fatalf("unverified profiles = %+v", got)
	}
}

func TestRegistrySnapshotRejectsStaleConnectivityPublication(t *testing.T) {
	registry := newTestRegistry()
	oldProvider := openaicompat.New("provider", "https://old.example/v1", "old-key", []modelinfo.Entry{{ID: "old"}})
	oldSnapshot := newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Providers:        map[string]modelcall.Provider{"provider": oldProvider},
		Entries:          map[string]CatalogEntry{"provider": {ID: "provider", BaseURL: "https://old.example/v1"}},
		CredentialValues: map[string]string{"provider": "old-key"},
		CredentialStored: map[string]bool{"provider": true},
		Configured:       map[string]bool{"provider": true},
	})
	registry.snapshot.Store(oldSnapshot)

	newProvider := openaicompat.New("provider", "https://new.example/v1", "new-key", []modelinfo.Entry{{ID: "new"}})
	newSnapshot := newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Providers:        map[string]modelcall.Provider{"provider": newProvider},
		Entries:          map[string]CatalogEntry{"provider": {ID: "provider", BaseURL: "https://new.example/v1"}},
		CredentialValues: map[string]string{"provider": "new-key"},
		CredentialStored: map[string]bool{"provider": true},
		Configured:       map[string]bool{"provider": true},
	})
	registry.snapshot.Store(newSnapshot)

	registry.publishConnectivityResult(oldSnapshot, "provider", []modelinfo.Entry{{ID: "stale"}})
	current := registry.snapshot.Load()
	if current != newSnapshot {
		t.Fatal("a stale connectivity result replaced the current provider generation")
	}
	if _, exists := current.observations["provider"]; exists {
		t.Fatal("a stale connectivity result authenticated the current credential")
	}
	if got := current.providers["provider"].Models(); len(got) != 1 || got[0].ID != "new" {
		t.Fatalf("current provider models = %+v, want new generation unchanged", got)
	}
}

func TestDiscoveryCacheKeySeparatesProviderGenerations(t *testing.T) {
	profile := providerprofile.Default()
	oldKey := discoveryCacheKey("provider", profile, "https://old.example/v1", "key-a")
	for name, key := range map[string]string{
		"endpoint":   discoveryCacheKey("provider", profile, "https://new.example/v1", "key-a"),
		"credential": discoveryCacheKey("provider", profile, "https://old.example/v1", "key-b"),
	} {
		if key == oldKey {
			t.Fatalf("%s change reused the prior discovery cache generation", name)
		}
	}
}

func TestModelEntryCloneOwnsNestedCapabilityEvidence(t *testing.T) {
	original := []modelinfo.Entry{{
		ID: "model",
		Capabilities: modelinfo.ModelCapabilities{
			Tools: modelinfo.CapabilityEvidence{State: modelinfo.CapabilitySupported, Sources: []string{"catalog"}},
		},
	}}
	cloned := modelinfo.CloneEntries(original)
	cloned[0].Capabilities.Tools.Sources[0] = "discovery"
	if got := original[0].Capabilities.Tools.Sources[0]; got != "catalog" {
		t.Fatalf("source mutation escaped clone: %q", got)
	}
}

func TestModelAssignmentDiscoveryFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(upstream.Close)
	catalog := mustCatalogCloneShipToLocal(t, `providers:
  - id: cloud
    kind: openai
    base_url: `+upstream.URL+`
    api_key_env: ""
    models: []
`+MinimalShipHTTPRetryYAML)
	registry, err := NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credentials.age")))
	if err == nil {
		registry.SetOutboundSecretScreen(NewModelSecretScreen(nil, nil))
	}
	testutil.FailErr(t, "create registry", err)
	service := &Service{Catalog: catalog, Registry: registry}
	ref := ModelRef{ProviderID: "cloud", Model: "candidate"}
	err = service.ValidateModelRef(t.Context(), ref, PolicySlotCoordinator)
	var unavailable *ModelCatalogUnavailableError
	if !errors.As(err, &unavailable) || unavailable.ProviderID != ref.ProviderID || unavailable.Model != ref.Model {
		t.Fatalf("assignment error = %v, want catalog unavailable for exact model", err)
	}
	if unavailable.Cause == nil || !errors.Is(err, unavailable.Cause) {
		t.Fatal("discovery cause was lost")
	}

	feed, err := modelfeed.New(modelfeed.Options{
		CacheDir: t.TempDir(),
		GetBytes: func(context.Context, string) ([]byte, error) {
			return []byte(`{"openai":{"id":"openai","models":{"candidate":{"id":"candidate","tool_call":true,"modalities":{"input":["text"],"output":["text"]}}}}}`), nil
		},
	})
	testutil.FailErr(t, "create fixture feed", err)
	_, err = feed.Refresh(t.Context())
	testutil.FailErr(t, "refresh fixture feed", err)
	registry.SetModelFeed(t.Context(), feed)
	testutil.FailErr(t, "assign known model despite failed discovery", service.ValidateModelRef(t.Context(), ref, PolicySlotCoordinator))
	ref.Model = "absent-from-feed"
	if err = service.ValidateModelRef(t.Context(), ref, PolicySlotCoordinator); !errors.As(err, &unavailable) {
		t.Fatalf("missing model with failed discovery = %v, want unavailable", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err = service.ValidateModelRef(ctx, ref, PolicySlotCoordinator); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled validation = %v", err)
	}
}

func TestModelAssignmentDiscoveryFailurePreservesRefusal(t *testing.T) {
	service := &Service{}
	ref := ModelRef{ProviderID: "cloud", Model: "candidate"}
	resolved := mergeResult{
		DiscoveryError: errors.New("fixture discovery failure"),
		Refused:        []modelinfo.Entry{{ID: ref.Model}},
	}
	err := service.unassignableModelError("openai", ref, resolved)
	var unavailable *ModelCatalogUnavailableError
	if err == nil || errors.As(err, &unavailable) {
		t.Fatalf("explicit refusal = %v, want assignment rejection", err)
	}
}

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

	reg, err := NewRegistry(t.Context(), cat, creds)

	if err == nil {

		reg.SetOutboundSecretScreen(NewModelSecretScreen(nil, nil))

	}
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
	reg.SetModelFeed(t.Context(), feed)

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
