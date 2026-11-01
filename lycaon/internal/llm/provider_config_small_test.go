package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
)

func TestDeviceConfigFilesUnchangedByOpen(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	configtest.Overlay(t, map[config.Rel]string{config.ModelPolicy: emptyModelPolicyYAML})

	localYAML := `providers:
  - id: keep-me
    kind: ollama
    base_url: http://localhost:11434/v1
    models:
      - id: gemma
`
	policyYAML := `coordinator:
  provider_id: fireworks-1
  model: glm
lite:
  provider_id: ollama-1
  model: gemma4
agent_pool:
  selection: first
  models:
    - provider_id: fireworks-1
      model: glm
`
	localPath := filepath.Join(configDir, "providers.local.yaml")
	policyPath := filepath.Join(configDir, "model-policy.yaml")
	testutil.FailErr(t, "write providers", os.WriteFile(localPath, []byte(localYAML), 0o600))
	testutil.FailErr(t, "write policy", os.WriteFile(policyPath, []byte(policyYAML), 0o600))
	beforeLocal, err := os.ReadFile(localPath)
	testutil.FailErr(t, "read providers", err)
	beforePolicy, err := os.ReadFile(policyPath)
	testutil.FailErr(t, "read policy", err)

	catalog, err := NewProviderCatalog()
	testutil.FailErr(t, "open catalog", err)
	_, err = NewPolicyStore()
	testutil.FailErr(t, "open policy", err)
	_, err = NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(configDir, "credential-vault.age")))
	testutil.FailErr(t, "open registry", err)

	afterLocal, err := os.ReadFile(localPath)
	testutil.FailErr(t, "reread providers", err)
	if string(afterLocal) != string(beforeLocal) {
		t.Fatalf("opening catalog/registry rewrote providers.local.yaml")
	}
	afterPolicy, err := os.ReadFile(policyPath)
	testutil.FailErr(t, "reread policy", err)
	if string(afterPolicy) != string(beforePolicy) {
		t.Fatalf("opening policy store rewrote model-policy.yaml")
	}
}

func TestCloudflareReadinessRequiresAccountAndKey(t *testing.T) {
	for _, tc := range []struct {
		name          string
		account       string
		key           string
		configuration string
		configured    bool
	}{
		{"placeholder with key", "YOUR_ACCOUNT_ID", "test-token", "invalid", false},
		{"lowercase placeholder", "your_account_id", "test-token", "invalid", false},
		{"empty account", "", "test-token", "invalid", false},
		{"account without key", "account-1", "", "valid", false},
		{"account with key", "account-1", "test-token", "valid", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := &Registry{}
			state, configured := registry.entryReadiness(t.Context(), CatalogEntry{
				Kind: "cloudflare-workers-ai", RequiresAPIKey: true,
				BaseURL: "https://api.cloudflare.com/client/v4/accounts/" + tc.account + "/ai/v1",
			}, tc.key)
			if state.Configuration != tc.configuration || configured != tc.configured {
				t.Fatalf("readiness = %+v, configured = %v; want configuration %s, configured %v",
					state, configured, tc.configuration, tc.configured)
			}
		})
	}
}

func TestCloudflarePlaceholderWithSavedKeyIsNotAssignable(t *testing.T) {
	catalog := mustCatalogCloneShipToLocal(t, `providers:
  - id: cloudflare-workers-ai-1
    kind: cloudflare-workers-ai
    base_url: https://api.cloudflare.com/client/v4/accounts/YOUR_ACCOUNT_ID/ai/v1
    requires_api_key: true
    models:
      - id: "@cf/meta/llama-3.1-8b-instruct"
`+MinimalShipHTTPRetryYAML)
	creds := providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age"))
	testutil.FailErr(t, "save credential", creds.Set("cloudflare-workers-ai-1", "test-token"))
	registry, err := NewRegistry(t.Context(), catalog, creds)
	if err == nil {
		registry.SetOutboundSecretScreen(NewModelSecretScreen(nil, nil))
	}
	testutil.FailErr(t, "create registry", err)
	providers := registry.List(t.Context())
	if len(providers) != 1 {
		t.Fatalf("provider count = %d, want 1", len(providers))
	}
	provider := providers[0]
	if provider.Configured || provider.ReadyToAssign {
		t.Fatalf("placeholder provider is ready: configured=%v, assignable=%v",
			provider.Configured, provider.ReadyToAssign)
	}
	if !provider.CredentialPresent {
		t.Fatal("saved credential must remain visible while account setup is incomplete")
	}
}

func TestAvailabilityStreamPreservesCommittedHistoryAndControls(t *testing.T) {
	var mu sync.Mutex
	var requests [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		attempt := len(requests)
		mu.Unlock()
		if attempt <= 12 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"Unavailable"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Complete.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	provider := openaicompat.New("fixture", server.URL, "", nil).WithHTTPRetry(availabilityTestPolicy())
	request := modelcall.CompletionRequest{Model: "candidate", MaxTokens: 16384, Messages: []api.Message{
		{Role: api.MessageRoleUser, Content: "Apply the requested change."},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{
			ID: "committed-write", Name: "write", Args: map[string]any{"path": "result.txt", "content": "saved"},
		}}},
		{Role: api.MessageRoleTool, Content: "saved", ToolResult: &api.ToolResult{ToolCallID: "committed-write", Content: "saved"}},
	}}
	stream, err := provider.Stream(t.Context(), request)
	testutil.FailErr(t, "recover stream after server errors", err)
	completion, _, err := modelcall.CollectStream(stream)
	testutil.FailErr(t, "collect recovered completion", err)
	mu.Lock()
	defer mu.Unlock()
	if completion.Content != "Complete." || len(requests) != 13 {
		t.Fatalf("content=%q requests=%d", completion.Content, len(requests))
	}
	for _, body := range requests[1:] {
		if !bytes.Equal(body, requests[0]) {
			t.Fatal("availability retry changed the admitted request")
		}
	}
	var wire struct {
		Messages  []json.RawMessage `json:"messages"`
		MaxTokens int               `json:"max_tokens"`
	}
	testutil.FailErr(t, "decode retained request", json.Unmarshal(requests[0], &wire))
	if len(wire.Messages) != 3 || wire.MaxTokens != request.MaxTokens {
		t.Fatalf("history=%d output allowance=%d", len(wire.Messages), wire.MaxTokens)
	}
}

func availabilityTestPolicy() providerretry.ProviderHTTPRetry {
	return providerretry.ProviderHTTPRetry{MaxRetries: 2, MaxWaitMs: 10, BackoffMs: []int{1, 1},
		Statuses: []int{429, 500, 502, 503, 504}, WaitHeaders: []string{"Retry-After"},
		Availability: &providerretry.ProviderAvailabilityRetry{InitialMs: 1, MaxBackoffMs: 2}}
}

func TestLoadProviderConfigRejectsUnknownFields(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{
		config.Providers: "providers:\n  - id: test\n    knid: test\n",
	})
	if _, err := LoadProviderConfig(); err == nil {
		t.Fatal("unknown provider field was accepted")
	}
}

func TestProviderCatalogRejectsUnknownLocalFields(t *testing.T) {
	stageShipProviders(t, "providers: []\n")
	localPath := filepath.Join(t.TempDir(), "providers.local.yaml")
	writeProvidersLocal(t, localPath, []byte("providers:\n  - id: test\n    knid: test\n"))
	if _, err := NewProviderCatalogAt(localPath); err == nil {
		t.Fatal("unknown local provider field was accepted")
	}
}

func TestProviderCatalogRejectsLocalRetryProfileWithoutKindTemplate(t *testing.T) {
	stageShipProviders(t, "providers: []\n")
	localPath := filepath.Join(t.TempDir(), "providers.local.yaml")
	writeProvidersLocal(t, localPath, []byte("providers:\n  - id: test\n    http_retry_profile: hosted\n"))
	if _, err := NewProviderCatalogAt(localPath); err == nil {
		t.Fatal("local retry profile selection was accepted")
	}
}

// The Ollama kind ships its parser rejection grammars, and a live instance
// built from the catalog carries them into the driver.
func TestOllamaKindAttachesShippedToolCallRejections(t *testing.T) {
	cfg, err := LoadProviderConfig()
	testutil.FailErr(t, "load provider catalog", err)
	ollama := providerByID(cfg.Providers, "ollama")
	if ollama == nil || ollama.ToolCallRejections.Profile != "ollama" || ollama.ToolCallRejections.IsZero() {
		t.Fatalf("ollama kind policy = %+v", ollama)
	}
	for _, provider := range cfg.Providers {
		if provider.ToolCallRejectionProfile != "" && provider.Kind != "ollama" {
			t.Errorf("provider %s declares parser rejections without a driver that reads them", provider.ID)
		}
	}
	entry, err := resolveLocalEntry(ProviderEntry{ID: "desktop", Kind: "ollama", BaseURL: "http://localhost:11434/v1"}, map[string]ProviderEntry{"ollama": *ollama})
	testutil.FailErr(t, "resolve local ollama", err)
	p, err := newProviderForEntry(context.Background(), entry, "", entry.HTTPRetry, nil, nil)
	testutil.FailErr(t, "build ollama driver", err)
	cause := failure.ErrProviderToolCallUnparsed
	if typed := p.Profile().ToolCallRejections.Classify("desktop", "m", "unexpected end of JSON input", true, cause); typed == cause { //nolint:errorlint // identity: an unclassified cause is returned itself
		t.Fatal("driver lacks the kind's parser rejection grammar")
	}
}

func TestToolCallRejectionProfilesBelongToTheKindCatalog(t *testing.T) {
	ship := map[string]ProviderEntry{"ollama": {Kind: "ollama", ToolCallRejectionProfile: "ollama"}}
	if _, err := resolveLocalEntry(ProviderEntry{ID: "desktop", Kind: "ollama", ToolCallRejectionProfile: "custom"}, ship); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched local profile: %v", err)
	}
	if _, err := resolveLocalEntry(ProviderEntry{ID: "other", Kind: "unknown", ToolCallRejectionProfile: "ollama"}, ship); err == nil || !strings.Contains(err.Error(), "no provider kind template") {
		t.Fatalf("profile without a kind template: %v", err)
	}
	cfg := ProviderConfig{
		Providers:                 []ProviderEntry{{ID: "ollama", ToolCallRejectionProfile: "missing"}},
		ToolCallRejectionProfiles: map[string]providerprofile.ToolCallRejectionPolicy{},
	}
	if err := resolveProviderToolCallRejectionProfiles(&cfg); err == nil || !strings.Contains(err.Error(), "unknown tool_call_rejection_profile") {
		t.Fatalf("unknown profile: %v", err)
	}
	cfg.ToolCallRejectionProfiles["bad"] = providerprofile.ToolCallRejectionPolicy{}
	if err := resolveProviderToolCallRejectionProfiles(&cfg); err == nil || !strings.Contains(err.Error(), `"bad"`) {
		t.Fatalf("invalid profile: %v", err)
	}
}
