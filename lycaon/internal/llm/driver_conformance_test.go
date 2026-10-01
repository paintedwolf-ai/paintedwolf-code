package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type promptCacheExpect int

const (
	promptCacheCheckNone promptCacheExpect = iota
	promptCacheRequireKey
	promptCacheForbidKey
)

type openAICompatConformanceCase struct {
	kind              string
	skip              string
	wantBearer        bool
	wantAzureAPIKey   bool
	promptCache       promptCacheExpect
	cloudflareAccount bool
}

func openAICompatConformanceCases() []openAICompatConformanceCase {
	return []openAICompatConformanceCase{
		{kind: "openai", wantBearer: true, promptCache: promptCacheRequireKey},
		{kind: "openai-compatible", wantBearer: true, promptCache: promptCacheForbidKey},
		{kind: "fireworks", wantBearer: true, promptCache: promptCacheRequireKey},
		{kind: "together", wantBearer: true, promptCache: promptCacheForbidKey},
		{kind: "openrouter", wantBearer: true, promptCache: promptCacheRequireKey},
		{kind: "gemini", wantBearer: true, promptCache: promptCacheForbidKey},
		{kind: "azure", wantAzureAPIKey: true, promptCache: promptCacheRequireKey},
		{kind: "litellm-proxy", wantBearer: true, promptCache: promptCacheForbidKey},
		{kind: "lmstudio", wantBearer: true, promptCache: promptCacheForbidKey},
		{kind: "omlx", wantBearer: true, promptCache: promptCacheForbidKey},
		{kind: "cloudflare-workers-ai", wantBearer: true, promptCache: promptCacheForbidKey, cloudflareAccount: true},
		{kind: "vertex", wantBearer: true, promptCache: promptCacheForbidKey},
	}
}

func driverConformanceCoverage() map[string]string {
	covered := map[string]string{
		"anthropic":      "TestAnthropicDriverConformance",
		"bedrock":        "covered by unit wire tests in bedrock_test.go",
		"ollama":         "native driver; covered by ollama_test.go",
		"vertex-express": "TestVertexExpressDriverConformance",
	}
	for _, c := range openAICompatConformanceCases() {
		if c.skip != "" {
			covered[c.kind] = c.skip
		} else {
			covered[c.kind] = "TestOpenAICompatDriverConformance"
		}
	}
	return covered
}

func TestDriverFactoriesConformanceCoverage(t *testing.T) {
	covered := driverConformanceCoverage()
	for _, kind := range driverFactories.Kinds() {
		if _, ok := covered[kind]; !ok {
			t.Errorf("driverFactories[%q] has no conformance entry — add httptest matrix or explicit skip reason", kind)
		}
	}
}

func TestOpenAICompatDriverConformance(t *testing.T) {
	for _, tc := range openAICompatConformanceCases() {
		t.Run(tc.kind, func(t *testing.T) {
			if tc.skip != "" {
				t.Skip(tc.skip)
			}
			runOpenAICompatConformance(t, tc)
		})
	}
}

func runOpenAICompatConformance(t *testing.T, tc openAICompatConformanceCase) {
	t.Helper()
	const apiKey = "conformance-key"
	const sessionID = "sess-conformance"

	var gotAuth string
	var gotAzureKey string
	var gotBodies []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "chat/completions") {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		gotAzureKey = r.Header.Get("api-key")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		gotBodies = append(gotBodies, string(body))

		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "pong",
				},
			}},
			"usage": map[string]any{
				"prompt_tokens":     3,
				"completion_tokens": 1,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	baseURL := srv.URL
	if tc.cloudflareAccount {
		baseURL = srv.URL + "/accounts/conftest/ai/v1"
	}

	provider := buildConformanceProvider(t, tc.kind, baseURL, apiKey)
	completion, err := provider.Complete(context.Background(), modelcall.CompletionRequest{
		Model: "m",
		Messages: []api.Message{{
			Role:    api.MessageRoleUser,
			Content: "ping",
		}},
	})
	testutil.FailErr(t, "Complete", err)
	if completion.Content == "" && len(completion.ToolCalls) == 0 {
		t.Fatal("expected non-empty content or tool calls")
	}

	if tc.wantBearer {
		if gotAuth != "Bearer "+apiKey {
			t.Fatalf("Authorization = %q, want Bearer %q", gotAuth, apiKey)
		}
	}
	if tc.wantAzureAPIKey {
		if gotAzureKey != apiKey {
			t.Fatalf("api-key = %q, want %q", gotAzureKey, apiKey)
		}
		if gotAuth != "" {
			t.Fatalf("azure must not send Authorization, got %q", gotAuth)
		}
	}

	switch tc.promptCache {
	case promptCacheRequireKey, promptCacheForbidKey:
		if len(gotBodies) == 0 {
			t.Fatal("expected captured request body")
		}
		_, err := provider.Complete(context.Background(), modelcall.CompletionRequest{
			Model: "m",
			Messages: []api.Message{{
				Role:    api.MessageRoleUser,
				Content: "cache probe",
			}},
			Debug: modelcall.RequestDebug{SessionID: sessionID},
		})
		testutil.FailErr(t, "Complete with session", err)
		if len(gotBodies) < 2 {
			t.Fatal("expected second request body for prompt-cache probe")
		}
		body := gotBodies[1]
		hasKey := strings.Contains(body, `"prompt_cache_key":"`+sessionID+`"`) ||
			strings.Contains(body, `"prompt_cache_key": "`+sessionID+`"`)
		switch tc.promptCache {
		case promptCacheRequireKey:
			if !hasKey {
				t.Fatalf("expected prompt_cache_key for session %q in body: %s", sessionID, body)
			}
		case promptCacheForbidKey:
			if strings.Contains(body, "prompt_cache_key") {
				t.Fatalf("prompt_cache_key must not be sent for kind %q: %s", tc.kind, body)
			}
		case promptCacheCheckNone:
		}
	case promptCacheCheckNone:
	}
}

func buildConformanceProvider(t *testing.T, kind, baseURL, apiKey string) modelcall.Provider {
	t.Helper()
	entry := CatalogEntry{
		ID:      kind,
		Kind:    kind,
		BaseURL: baseURL,
		Models:  []modelinfo.Entry{{ID: "m"}},
	}
	policy := shippedPromptCache(t)[kind]
	switch kind {
	case "cloudflare-workers-ai":
		return attachPromptCache(openaicompat.NewCloudflare(entry.ID, entry.BaseURL, entry.Models, apiKey, openaicompat.NewCloudflareUsageCache(), nil), policy)
	case "vertex":
		return attachPromptCache(openaicompat.New(entry.ID, baseURL, apiKey, entry.Models).
			WithProfile(providerprofile.Vertex()), policy)
	default:
		provider, err := driverFactories.Build(context.Background(), kind, providerBuild{
			entry: entry, apiKey: apiKey, resolved: ResolveProvider(entry, apiKey),
		})
		if err != nil {
			t.Fatalf("build conformance provider: %v", err)
		}
		return attachPromptCache(provider, policy)
	}
}
