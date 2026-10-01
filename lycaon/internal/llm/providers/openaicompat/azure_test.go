package openaicompat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAzureChatCompletionURLUsesV1Route(t *testing.T) {
	p := New("azure", "https://acme.openai.azure.com/openai/v1", "k", nil).
		WithProfile(providerprofile.Azure()).
		WithHTTPProfile(AzureHTTPProfile())

	got := p.chatCompletionURL("gpt-4o-prod")
	want := "https://acme.openai.azure.com/openai/v1/chat/completions"
	if got != want {
		t.Fatalf("azure url = %q, want %q", got, want)
	}
}

func TestOpenAIChatCompletionURLUnchanged(t *testing.T) {
	p := New("openai", "https://api.openai.com/v1", "k", nil)
	if got := p.chatCompletionURL("gpt-4o"); got != "https://api.openai.com/v1/chat/completions" {
		t.Fatalf("openai url = %q", got)
	}
}

func TestAzureAuthUsesAPIKeyHeader(t *testing.T) {
	var gotHeader, gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeader = r.Header.Get("api-key")
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	p := New("azure", srv.URL+"/openai/v1", "secret-key", nil).
		WithProfile(providerprofile.Azure()).
		WithHTTPProfile(AzureHTTPProfile())

	resp, err := p.postChat(t.Context(), "dep1", []byte(`{}`), p.completeClient, nil)
	if err != nil {
		t.Fatalf("postChat: %v", err)
	}
	_ = resp.Body.Close()

	if gotHeader != "secret-key" {
		t.Fatalf("api-key header = %q, want secret-key", gotHeader)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization header should be empty for azure, got %q", gotAuth)
	}
	if gotPath != "/openai/v1/chat/completions" {
		t.Fatalf("path = %q, want Azure v1 chat route", gotPath)
	}
}

func TestAzureRequestUsesMaxCompletionTokens(t *testing.T) {
	p := New("azure", "https://acme.openai.azure.com", "k", []modelinfo.Entry{
		{ID: "reasoning-deployment", MaxTokens: 8192, ReasoningEffort: "high"},
	}).WithProfile(providerprofile.Azure()).WithHTTPProfile(AzureHTTPProfile())

	body, err := encodeChatCompletionRequest(modelcall.CompletionRequest{
		Model: "reasoning-deployment",
	}, p, false, controlOpts{})
	testutil.FailErr(t, "encode azure request", err)
	var wire map[string]any
	testutil.FailErr(t, "decode azure request", json.Unmarshal(body, &wire))
	if wire["max_completion_tokens"] != float64(8192) {
		t.Fatalf("max_completion_tokens = %v, want 8192; body=%s", wire["max_completion_tokens"], body)
	}
	if _, exists := wire["max_tokens"]; exists {
		t.Fatalf("azure request emitted unsupported max_tokens: %s", body)
	}
}
