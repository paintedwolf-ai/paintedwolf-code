package llm

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRegistryReadsDoNotWaitForRebuildDiscovery(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var tagCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		if tagCalls.Add(1) > 1 {
			select {
			case <-started:
			default:
				close(started)
			}
			<-release
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{}})
	}))
	t.Cleanup(server.Close)

	yaml := "providers:\n  - id: ollama\n    kind: ollama\n    base_url: " + server.URL + "/v1\n    api_key_env: \"\"\n    models: []\n" + MinimalShipHTTPRetryYAML
	catalog := mustCatalogCloneShipToLocal(t, yaml)
	registry, err := NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
	if err == nil {
		registry.SetOutboundSecretScreen(NewModelSecretScreen(nil, nil))
	}
	testutil.FailErr(t, "NewRegistry", err)

	reloadDone := make(chan error, 1)
	go func() { reloadDone <- registry.Reload(t.Context()) }()
	select {
	case <-started:
	case <-t.Context().Done():
		t.Fatal("rebuild did not enter provider discovery")
	}

	getDone := make(chan error, 1)
	go func() {
		_, getErr := registry.Get("ollama")
		getDone <- getErr
	}()
	select {
	case getErr := <-getDone:
		testutil.FailErr(t, "Registry.Get during rebuild", getErr)
	case <-t.Context().Done():
		t.Fatal("Registry.Get waited for provider discovery")
	}

	releaseOnce.Do(func() { close(release) })
	testutil.FailErr(t, "Registry.Reload", <-reloadDone)
}

func TestRefreshModelsRechecksAmbientProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(server.Close)
	yaml := "providers:\n  - id: ambient\n    kind: openai-compatible\n    base_url: " + server.URL + "/v1\n    api_key_env: \"\"\n    ambient_auth: test-chain\n    models:\n      - id: model\n" + MinimalShipHTTPRetryYAML
	catalog := mustCatalogCloneShipToLocal(t, yaml)
	registry, err := NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
	if err == nil {
		registry.SetOutboundSecretScreen(NewModelSecretScreen(nil, nil))
	}
	testutil.FailErr(t, "NewRegistry", err)

	current := registry.snapshot.Load()
	configured := current.cloneConfigured()
	configured["ambient"] = false
	readiness := current.cloneReadiness()
	readiness["ambient"] = providerReadiness{Configuration: "valid", Authentication: "missing"}
	registry.snapshot.Store(newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Providers: current.cloneProviders(), Entries: current.cloneEntries(), CredentialValues: current.cloneCredentialValues(),
		CredentialStored: current.cloneCredentialStored(), DefaultID: current.defaultID,
		Configured: configured, Readiness: readiness, Observations: current.cloneObservations(),
	}))

	err = registry.RefreshModels(t.Context(), "ambient")
	testutil.FailErr(t, "RefreshModels", err)
	if !registry.IsConfigured("ambient") {
		t.Fatal("ambient provider did not recover from stale missing-auth state")
	}
}

func TestEnsureConfiguredRecoversWhenAmbientCredentialsAppear(t *testing.T) {
	credentialPath := filepath.Join(t.TempDir(), "application-default-credentials.json")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credentialPath)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	yaml := `providers:
  - id: vertex
    kind: vertex
    base_url: us-central1
    api_key_env: ""
    requires_api_key: false
    ambient_auth: google-adc
    models:
      - id: google/test-model
` + MinimalShipHTTPRetryYAML
	catalog := mustCatalogCloneShipToLocal(t, yaml)
	registry, err := NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
	if err == nil {
		registry.SetOutboundSecretScreen(NewModelSecretScreen(nil, nil))
	}
	testutil.FailErr(t, "NewRegistry", err)
	if registry.IsConfigured("vertex") {
		t.Fatal("vertex unexpectedly configured before credentials exist")
	}

	credentials := []byte(`{
  "type": "authorized_user",
  "client_id": "test-client",
  "client_secret": "test-secret",
  "refresh_token": "test-refresh-token"
}`)
	testutil.FailErr(t, "write ambient credentials", os.WriteFile(credentialPath, credentials, 0o600))
	configured, err := registry.ensureConfigured(t.Context(), "vertex")
	testutil.FailErr(t, "ensureConfigured", err)
	if !configured || !registry.IsConfigured("vertex") {
		t.Fatal("vertex did not recover after ambient credentials appeared")
	}
}

func TestEffectiveModelsDoesNotRepeatCachedDiscoveryFailure(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	yaml := "providers:\n  - id: hosted\n    kind: openai-compatible\n    base_url: " + server.URL + "/v1\n    api_key_env: HOSTED_KEY\n    models:\n      - id: model\n" + MinimalShipHTTPRetryYAML
	catalog := mustCatalogCloneShipToLocal(t, yaml)
	credentials := providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age"))
	testutil.FailErr(t, "store credential", credentials.Set("hosted", "secret"))
	registry, err := NewRegistry(t.Context(), catalog, credentials)
	if err == nil {
		registry.SetOutboundSecretScreen(NewModelSecretScreen(nil, nil))
	}
	testutil.FailErr(t, "NewRegistry", err)

	_ = registry.EffectiveModels(t.Context(), "hosted")
	_ = registry.EffectiveModels(t.Context(), "hosted")
	if got := requests.Load(); got != 1 {
		t.Fatalf("discovery requests = %d, want one cached failure", got)
	}
}

func TestRegistryUnwiredScreenRefusesEveryRequest(t *testing.T) {
	inner := &responseRetryStub{}
	registry := &Registry{}
	registry.snapshot.Store(newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Providers: map[string]modelcall.Provider{inner.ID(): inner}, DefaultID: inner.ID(),
	}))
	selected, err := registry.Get(inner.ID())
	testutil.FailErr(t, "get provider", err)
	for _, provider := range []modelcall.Provider{selected, registry.Default()} {
		if provider.ID() != inner.ID() || !reflect.DeepEqual(provider.Models(), inner.Models()) || !reflect.DeepEqual(provider.Profile(), inner.Profile()) {
			t.Fatal("unwired screen changed catalog metadata")
		}
		_, completeErr := provider.Complete(t.Context(), modelcall.CompletionRequest{})
		_, streamErr := provider.Stream(t.Context(), modelcall.CompletionRequest{})
		for _, err := range []error{completeErr, streamErr} {
			var fault *ModelRequestSecretScreenFaultError
			if !errors.As(err, &fault) || fault.Stage != secretmatch.FaultStageScreenUnwired || !SecretScreenBlocked(err) {
				t.Fatalf("request error = %v, want unwired screen fault", err)
			}
		}
	}
	if inner.completeCalls != 0 || inner.streamCalls != 0 {
		t.Fatalf("unscreened dispatch: complete=%d stream=%d", inner.completeCalls, inner.streamCalls)
	}
	registry.SetOutboundSecretScreen(NewModelSecretScreen(nil, nil))
	provider, err := registry.Get(inner.ID())
	testutil.FailErr(t, "get explicitly screened provider", err)
	if _, ok := provider.(*lifecycleProvider); !ok {
		t.Fatal("screen installation lost lifecycle wrapper")
	}
}
