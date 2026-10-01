package llm

import (
	"context"
	"path/filepath"
	"testing"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProviderUnconfiguredFailsWithoutMockEnv(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "")

	fallback := NewMockProvider(mockConfigForTest(t))
	client := NewRoutingClient(nil, nil, fallback, nil, nil, nil, func(context.Context) (*ModelSelection, error) {
		return &ModelSelection{ProviderID: "failing", Model: "gpt-4o"}, nil
	})

	_, err := client.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected error when provider is not configured")
	}
	if _, ok := failure.AsProviderNotConfigured(err); !ok {
		t.Fatalf("expected provider not configured, got %v", err)
	}
}

func TestRoutingClientIgnoresWiredMockWithoutEnv(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "")

	mock := NewMockProvider(mockConfigForTest(t))
	client := NewRoutingClient(nil, nil, mock, nil, nil, nil, func(context.Context) (*ModelSelection, error) {
		return &ModelSelection{ProviderID: "needs-key", Model: "gpt-4o"}, nil
	})

	_, err := client.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected error, not mock completion")
	}
	if _, ok := failure.AsProviderNotConfigured(err); !ok {
		t.Fatalf("expected provider not configured, got %v", err)
	}
}

func TestRoutingClientRegistryUnconfiguredProvider(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "")
	t.Setenv("OPENAI_API_KEY", "")

	tmp := t.TempDir()
	shipYAML := `providers:
  - id: openai-compatible
    base_url: https://api.openai.com/v1
    api_key_env: OPENAI_API_KEY
    models:
      - id: gpt-4o
    http_retry:
      max_retries: 1
      max_wait_ms: 1000
      backoff_ms: [1]
      statuses: [429]
      wait_headers: [Retry-After]
`
	stageShipProviders(t, shipYAML)
	localPath := filepath.Join(tmp, "providers.local.yaml")
	writeProvidersLocal(t, localPath, []byte(shipYAML))
	catalog, err := NewProviderCatalogAt(localPath)
	testutil.FailErr(t, "NewProviderCatalogAt failed", err)
	registry, err := NewRegistry(catalog, providercredentials.NewAt(filepath.Join(tmp, "credential-vault.age")))
	testutil.FailErr(t, "NewRegistry failed", err)

	mock := NewMockProvider(mockConfigForTest(t))
	client := NewRoutingClient(registry, nil, mock, nil, nil, nil, func(context.Context) (*ModelSelection, error) {
		return &ModelSelection{ProviderID: "openai-compatible", Model: "gpt-4o"}, nil
	})

	_, err = client.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected error for unconfigured openai-compatible")
	}
	pe, ok := failure.AsProviderNotConfigured(err)
	if !ok {
		t.Fatalf("expected provider not configured, got %v", err)
	}
	if pe.ProviderID != "openai-compatible" {
		t.Fatalf("provider id = %q", pe.ProviderID)
	}
}

func TestStreamUnconfiguredProviderFails(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "")

	client := NewRoutingClient(nil, nil, NewMockProvider(mockConfigForTest(t)), nil, nil, nil, func(context.Context) (*ModelSelection, error) {
		return &ModelSelection{ProviderID: "missing", Model: "gpt-4o"}, nil
	})
	_, err := client.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected stream error")
	}
	if _, ok := failure.AsProviderNotConfigured(err); !ok {
		t.Fatalf("expected provider not configured, got %v", err)
	}
}

func TestRoutingClientMockOnlyEnv(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")

	mock := NewMockProvider(mockConfigForTest(t))
	client := NewRoutingClient(nil, nil, mock, nil, nil, nil, func(context.Context) (*ModelSelection, error) {
		return &ModelSelection{ProviderID: "ollama", Model: "llama3.1"}, nil
	})

	completion, err := client.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
	})
	testutil.FailErr(t, "client.Complete failed", err)
	if completion == nil || completion.Content == "" {
		t.Fatal("expected mock completion")
	}
	if completion.ProviderID != "mock" || completion.Model != "mock" || !completion.Fallback {
		t.Fatalf("completion meta = %+v", completion)
	}
}

func TestRoutingClientMockStreamCarriesFallbackSelection(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")

	mock := NewMockProvider(mockConfigForTest(t))
	client := NewRoutingClient(nil, nil, mock, nil, nil, nil, func(context.Context) (*ModelSelection, error) {
		return &ModelSelection{ProviderID: "configured", Model: "configured"}, nil
	})
	ch, err := client.Stream(t.Context(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
	})
	testutil.FailErr(t, "Stream", err)
	completion, _, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "CollectStream", err)
	if completion.ProviderID != "mock" || completion.Model != "mock" || !completion.Fallback {
		t.Fatalf("completion meta = %+v", completion)
	}
}

func TestConfiguredProviderEmptyResponseFails(t *testing.T) {
	// Disable development routing for this provider test.
	t.Setenv("LYCAON_LLM_MOCK", "")
	tmp := t.TempDir()
	shipYAML := `providers:
  - id: empty-provider
    base_url: http://127.0.0.1:1
    api_key_env: ""
    models:
      - id: test-model
    http_retry:
      max_retries: 1
      max_wait_ms: 1000
      backoff_ms: [1]
      statuses: [429]
      wait_headers: [Retry-After]
`
	stageShipProviders(t, shipYAML)
	localPath := filepath.Join(tmp, "providers.local.yaml")
	writeProvidersLocal(t, localPath, []byte(shipYAML))
	catalog, err := NewProviderCatalogAt(localPath)
	testutil.FailErr(t, "NewProviderCatalogAt failed", err)
	registry, err := NewRegistry(catalog, providercredentials.NewAt(filepath.Join(tmp, "credential-vault.age")))
	testutil.FailErr(t, "NewRegistry failed", err)

	fallback := NewMockProvider(mockConfigForTest(t))
	client := NewRoutingClient(registry, nil, fallback, nil, nil, nil, func(context.Context) (*ModelSelection, error) {
		return &ModelSelection{ProviderID: "empty-provider", Model: "test-model"}, nil
	})

	_, err = client.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected error when configured provider returns empty")
	}
}

func mockConfigForTest(t *testing.T) *MockConfig {
	t.Helper()
	cfg, err := LoadMockConfig()
	if err != nil {
		t.Fatalf("load mock config: %v", err)
	}
	return cfg
}
