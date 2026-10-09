package contractfixture

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func CreateProviderJSON(t *testing.T, base, body string) wire.ProviderMeta {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/v1/providers", strings.NewReader(body))
	testutil.FailErr(t, "new provider create", err)
	req.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(req)
	resp, err := fixtureClient.Do(req)
	testutil.FailErr(t, "create provider", err)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", resp.StatusCode, ReadBody(t, resp))
	}
	var meta wire.ProviderMeta
	testutil.FailErr(t, "decode created provider", json.NewDecoder(resp.Body).Decode(&meta))
	return meta
}

func FakeModelPolicyYAML() string {
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

func FakeProvidersYAML(baseURL string) string {
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

func JsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

func NewProvADiscoveryServer(t *testing.T) *httptest.Server {
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

func NewProviderTestServer(t *testing.T) (*hostapi.Server, string) {
	t.Helper()
	discoveryServer := NewProvADiscoveryServer(t)
	return NewProviderTestServerAt(t, discoveryServer.URL)
}

func NewProviderTestServerAt(t *testing.T, discoveryBaseURL string, opts ...TestDeps) (*hostapi.Server, string) {
	t.Helper()
	providersYAML := FakeProvidersYAML(discoveryBaseURL)
	return NewProviderTestServerWithCatalogs(t, providersYAML, providersYAML, opts...)
}

func NewProviderTestServerWithCatalogs(t *testing.T, shipYAML, localYAML string, opts ...TestDeps) (*hostapi.Server, string) {
	t.Helper()
	tmp := t.TempDir()
	// Live instances and global policy use host files.
	configtest.Overlay(t, map[config.Rel]string{
		config.Providers:   shipYAML,
		config.ModelPolicy: FakeModelPolicyYAML(),
	})

	userProviders := filepath.Join(tmp, "providers.local.yaml")
	credPath := filepath.Join(tmp, "credential-vault.age")
	globalPolicy := filepath.Join(tmp, "model-policy.yaml")
	// Bundled provider kinds remain templates.
	if err := os.WriteFile(userProviders, []byte(localYAML), 0o600); err != nil {
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
		Mock:        llm.NewMockProvider(TestMockConfig(t)),
	}

	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	store := store.NewMemory()
	mock := llm.NewMockProvider(TestMockConfig(t))
	mgr := session.NewHost(store, session.Models{Client: mock, Provider: svc, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	hub := events.NewMemoryHub()
	deps := hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: store, Projects: project.NewMemoryRegistry(), Sessions: mgr}, Providers: hostapi.ProvidersDependencies{LLM: svc}, Host: hostapi.HostDependencies{Events: hub}}
	for _, opt := range opts {
		opt(&deps)
	}
	srv := hostapi.NewServer(RequiredTestDeps(t, deps), nil, hostapi.TestAPIToken)
	return srv, StartTestHTTPServer(t, srv)
}

func PutProviderJSON(t *testing.T, base, id, body string) wire.ProviderMeta {
	t.Helper()
	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodPatch, base+"/v1/providers/"+id, strings.NewReader(body),
	)
	testutil.FailErr(t, "new provider update", err)
	req.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(req)
	resp, err := fixtureClient.Do(req)
	testutil.FailErr(t, "update provider", err)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT %s status = %d body = %s", body, resp.StatusCode, ReadBody(t, resp))
	}
	var meta wire.ProviderMeta
	testutil.FailErr(t, "decode provider meta", json.NewDecoder(resp.Body).Decode(&meta))
	return meta
}

// Trust follows the resolved destination, including partial updates.

const TestProviderID = "prov-a"
