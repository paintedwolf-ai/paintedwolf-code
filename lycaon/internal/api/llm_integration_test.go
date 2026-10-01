//go:build integration

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/cost/costtest"
	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// minimalShipHTTPRetryYAML matches llm test fixtures for synthetic ship catalogs.
const minimalShipHTTPRetryYAML = `    http_retry:
      max_retries: 1
      max_wait_ms: 1000
      backoff_ms: [1]
      statuses: [429]
      wait_headers: [Retry-After]
`

func TestProviderPromptAgainstStubBackend(t *testing.T) {
	// Disable mock routing so the request reaches the stub backend.
	t.Setenv("LYCAON_LLM_MOCK", "")
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "Provider response"}},
			},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2},
		})
	}))
	defer mockServer.Close()

	tmp := t.TempDir()
	providersYAML := "providers:\n  - id: test-openai\n    base_url: " + mockServer.URL + "\n    api_key_env: TEST_PHASE04_KEY\n    models:\n      - id: gpt-test\n        input_per_1k: 0.001\n        output_per_1k: 0.002\n        currency: USD\n" + minimalShipHTTPRetryYAML
	configtest.Overlay(t, map[config.Rel]string{config.Providers: providersYAML})
	localProviders := filepath.Join(tmp, "providers.local.yaml")
	if err := os.WriteFile(localProviders, []byte(providersYAML), 0o644); err != nil {
		testutil.FailErr(t, "write providers.local", err)
	}

	catalog, err := llm.NewProviderCatalogAt(localProviders)
	testutil.FailErr(t, "llm.NewProviderCatalogAt failed", err)
	creds := providercredentials.NewAt(filepath.Join(tmp, "credential-vault.age"))
	if err := creds.Set("test-openai", "test-key"); err != nil {
		testutil.FailErr(t, "creds.Set failed", err)
	}
	registry, err := llm.NewRegistry(catalog, creds)
	testutil.FailErr(t, "llm.NewRegistry failed", err)
	policy, err := llm.NewPolicyStoreAt(filepath.Join(tmp, "model-policy.yaml"))
	testutil.FailErr(t, "llm.NewPolicyStoreAt failed", err)
	policy.PutGlobal(llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "test-openai", Model: "gpt-test"},
		Lite:        llm.ModelRef{ProviderID: "test-openai", Model: "gpt-test"},
	})

	svc := &llm.Service{
		Catalog:     catalog,
		Credentials: creds,
		Registry:    registry,
		Policy:      policy,
		Router:      llm.NewStaticModelRouter(policy),
		Mock:        llm.NewMockProvider(testMockConfig(t)),
	}

	tracker := costtest.NewTracker(t, cost.NoopPricer{})
	store := store.NewMemory()
	mock := llm.NewMockProvider(testMockConfig(t))
	mgr := session.NewManagerWithLLMService(store, mock, svc, tools.NewStubRegistry(), settings.DefaultSessionLimits(), tracker)
	srv := NewServer(requiredTestDeps(t, Dependencies{Store: store, Projects: project.NewMemoryRegistry(), Sessions: mgr, LLM: svc}), nil, TestAPIToken)
	baseURL := startTestHTTPServer(t, srv)

	dir := t.TempDir()
	sess := createTestSession(t, baseURL, dir)
	acceptPrompt(t, baseURL, sess.ID, "hello")
	_, streamURL := waitForAssistantStream(t, baseURL, sess.ID, 10*time.Second)

	content, sawDone := readSSEStream(t, baseURL+streamURL)
	if !sawDone {
		t.Fatal("expected done chunk")
	}
	if !strings.Contains(content, "Provider response") {
		t.Fatalf("stream content = %q", content)
	}

	summary, err := tracker.Summary(t.Context(), wire.CostScopeSession, sess.ID, "")
	testutil.FailErr(t, "tracker.Summary failed", err)
	if summary.TokenTotals.Prompt+summary.TokenTotals.Completion < 5 {
		t.Fatalf("expected usage recorded, got %+v", summary)
	}
}

func TestProductionPromptWithoutProvider(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "")
	t.Setenv("TEST_PHASE04_KEY", "")

	tmp := t.TempDir()
	providersYAML := `providers:
  - id: test-openai
    base_url: https://example.invalid/v1
    api_key_env: TEST_PHASE04_KEY
    models:
      - id: gpt-test
    http_retry:
      max_retries: 1
      max_wait_ms: 1000
      backoff_ms: [1]
      statuses: [429]
      wait_headers: [Retry-After]
`
	configtest.Overlay(t, map[config.Rel]string{config.Providers: providersYAML})
	policyYAML := `coordinator:
  provider_id: test-openai
  model: gpt-test
lite:
  provider_id: test-openai
  model: gpt-test
agent_pool:
  selection: round_robin
  models: []
`
	configtest.Overlay(t, map[config.Rel]string{config.ModelPolicy: policyYAML})

	catalog, err := llm.NewProviderCatalogAt(filepath.Join(tmp, "providers.local.yaml"))
	testutil.FailErr(t, "llm.NewProviderCatalogAt failed", err)
	registry, err := llm.NewRegistry(catalog, providercredentials.NewAt(filepath.Join(tmp, "credential-vault.age")))
	testutil.FailErr(t, "llm.NewRegistry failed", err)
	policy, err := llm.NewPolicyStoreAt(filepath.Join(tmp, "global-policy.yaml"))
	testutil.FailErr(t, "llm.NewPolicyStoreAt failed", err)

	svc := &llm.Service{
		Catalog:     catalog,
		Credentials: providercredentials.NewAt(filepath.Join(tmp, "credential-vault.age")),
		Registry:    registry,
		Policy:      policy,
		Router:      llm.NewStaticModelRouter(policy),
	}

	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	store := store.NewMemory()
	mgr := session.NewManagerWithLLMService(store, nil, svc, tools.NewStubRegistry(), settings.DefaultSessionLimits(), nil)
	srv := NewServer(requiredTestDeps(t, Dependencies{Store: store, Projects: project.NewMemoryRegistry(), Sessions: mgr, LLM: svc}), nil, TestAPIToken)
	baseURL := startTestHTTPServer(t, srv)

	dir := t.TempDir()
	sess := createTestSession(t, baseURL, dir)

	acceptPrompt(t, baseURL, sess.ID, "hello")
	waitForSessionIdle(t, baseURL, sess.ID, 10*time.Second)

	msgs := getSessionMessages(t, baseURL, sess.ID)
	if len(msgs) != 1 || msgs[0].Role != wire.MessageRoleUser {
		t.Fatalf("messages = %+v, want single user message only", msgs)
	}
}

func getSessionMessages(t *testing.T, baseURL, sessionID string) []wire.Message {
	t.Helper()
	resp, err := authedHTTPGet(baseURL + "/v1/sessions/" + sessionID + "/messages")
	testutil.FailErr(t, "authedHTTPGet failed", err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET messages status = %d", resp.StatusCode)
	}
	var page wire.SessionTranscriptPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	msgs := page.Messages
	return msgs
}
