package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
)

func fakeModelPolicyYAML() string {
	return `coordinator:
  provider_id: prov-a
  model: model-x

lite:
  provider_id: prov-a
  model: model-y

agent_pool:
  selection: round_robin
  models:
    - provider_id: prov-a
      model: model-y
    - provider_id: prov-a
      model: model-x
`
}

func fakeProvidersYAML(baseURL string) string {
	return `providers:
  - id: prov-a
    base_url: ` + baseURL + `
    api_key_env: ""
    models:
      - id: model-x
        input_per_1k: 0.001
        output_per_1k: 0.002
        currency: USD
        capabilities:
          chat: {state: supported}
          tools: {state: supported}
      - id: model-y
        input_per_1k: 0.0005
        output_per_1k: 0.001
        currency: USD
        capabilities:
          chat: {state: supported}
          tools: {state: supported}
    http_retry:
      max_retries: 1
      max_wait_ms: 1000
      backoff_ms: [1]
      statuses: [429]
      wait_headers: [Retry-After]
`
}

// newProvADiscoveryServer serves fixture discovery models.

func newProvADiscoveryServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"model-x"},{"id":"model-y"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newProviderTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	discoveryServer := newProvADiscoveryServer(t)
	return newProviderTestServerAt(t, discoveryServer.URL)
}

func newProviderTestServerAt(t *testing.T, discoveryBaseURL string, opts ...testDeps) (*Server, string) {
	t.Helper()
	providersYAML := fakeProvidersYAML(discoveryBaseURL)
	return newProviderTestServerWithCatalogs(t, providersYAML, providersYAML, opts...)
}

func newProviderTestServerWithCatalogs(t *testing.T, shipYAML, localYAML string, opts ...testDeps) (*Server, string) {
	t.Helper()
	tmp := t.TempDir()
	// Live instances and global policy use host files.
	configtest.Overlay(t, map[config.Rel]string{
		config.Providers:   shipYAML,
		config.ModelPolicy: fakeModelPolicyYAML(),
	})

	userProviders := filepath.Join(tmp, "providers.local.yaml")
	credPath := filepath.Join(tmp, "credential-vault.age")
	globalPolicy := filepath.Join(tmp, "model-policy.yaml")
	// Bundled provider kinds remain templates.
	if err := os.WriteFile(userProviders, []byte(localYAML), 0o644); err != nil {
		testutil.FailErr(t, "write providers.local", err)
	}

	catalog, err := llm.NewProviderCatalogAt(userProviders)
	testutil.FailErr(t, "load provider catalog", err)
	creds := providercredentials.NewAt(credPath)
	policy, err := llm.NewPolicyStoreAt(globalPolicy)
	testutil.FailErr(t, "load model policy", err)
	registry, err := llm.NewRegistry(catalog, creds)
	testutil.FailErr(t, "build provider registry", err)
	svc := &llm.Service{
		Catalog:     catalog,
		Credentials: creds,
		Registry:    registry,
		Policy:      policy,
		Router:      llm.NewStaticModelRouter(policy),
		Mock:        llm.NewMockProvider(testMockConfig(t)),
	}

	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	store := store.NewMemory()
	mock := llm.NewMockProvider(testMockConfig(t))
	mgr := session.NewManagerWithLLMService(store, mock, svc, nil, settings.DefaultSessionLimits(), nil)
	hub := events.NewMemoryHub()
	deps := Dependencies{Core: CoreDependencies{Store: store, Projects: project.NewMemoryRegistry(), Sessions: mgr}, Providers: ProvidersDependencies{LLM: svc}, Host: HostDependencies{Events: hub}}
	for _, opt := range opts {
		opt(&deps)
	}
	srv := NewServer(requiredTestDeps(t, deps), nil, TestAPIToken)
	return srv, startTestHTTPServer(t, srv)
}

type recordingInventoryService struct {
	mu       sync.Mutex
	requests []sourceledger.InventoryRequest
}

func (f *recordingInventoryService) EnsureInventory(_ context.Context, req sourceledger.InventoryRequest) error {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	return nil
}

func (f *recordingInventoryService) snapshot() []sourceledger.InventoryRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sourceledger.InventoryRequest(nil), f.requests...)
}

const testProviderID = "prov-a"

func withSessionStore(store session.Store) testDeps {
	return func(d *Dependencies) { d.Core.Store = store }
}

// withWorkers serves worker jobs from queue.

func (*recordingInventoryService) SuspendInventory(context.Context, string) (func(), error) {
	return func() {}, nil
}
func (*recordingInventoryService) ObservePaths(context.Context, string, []sourceledger.RootSpec, []sourceledger.PathRef) (int, error) {
	return 0, nil
}
