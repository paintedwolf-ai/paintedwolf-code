package vertexexpress

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func vertexExpressTestProvider(models ...modelinfo.Entry) *Provider {
	if len(models) == 0 {
		models = []modelinfo.Entry{{ID: "gemini-2.5-flash"}}
	}
	return New("vertex-express", "https://aiplatform.googleapis.com/v1", "k", models)
}

// Express mode addresses models on the global host with no project or location
// segment.
func TestVertexExpressModelURLOmitsProjectAndLocation(t *testing.T) {
	p := vertexExpressTestProvider()

	got := p.modelURL("gemini-2.5-flash", "generateContent", false)
	want := "https://aiplatform.googleapis.com/v1/publishers/google/models/gemini-2.5-flash:generateContent"
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}

	stream := p.modelURL("gemini-2.5-flash", "streamGenerateContent", true)
	if !strings.HasSuffix(stream, ":streamGenerateContent?alt=sse") {
		t.Fatalf("stream url = %q, want the alt=sse SSE framing hint", stream)
	}
	for _, forbidden := range []string{"/projects/", "/locations/", "-aiplatform.googleapis.com"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("url = %q, must not contain %q", got, forbidden)
		}
	}
}

func TestVertexExpressDriverProfileShape(t *testing.T) {
	profile := vertexExpressTestProvider().Profile()
	if profile.Discovery != providerprofile.DiscoveryVertexExpress {
		t.Errorf("discovery = %q, want vertex_express", profile.Discovery)
	}
	if profile.Thinking != modelinfo.ThinkStyleBudgetTokens {
		t.Errorf("thinking = %q, want budget_tokens", profile.Thinking)
	}
	if profile.RoundTripsToolCallID() {
		t.Error("tool-call ids must stay host-managed — a Gemini functionCall carries no id")
	}
	if profile.PromptCache.Mode.Caches() {
		t.Errorf("prompt cache = %q, want none before the catalog attaches a policy", profile.PromptCache.Mode)
	}
}

// Gemini 2.5 retains its native budget control alongside Gemini 3 levels.
func TestVertexExpressThinkingUsesBudgetsForGemini25(t *testing.T) {
	profile := providerprofile.VertexExpress()
	got := modelcall.ResolveModelThinking(profile, modelinfo.Entry{ID: "gemini-2.5-flash"}, "gemini-2.5-flash")
	if got.Style != modelinfo.ThinkStyleBudgetTokens {
		t.Fatalf("style = %q, want budget_tokens", got.Style)
	}
}

// Explicit off omits unsupported zero budgets.
func TestVertexExpressThinkingConfigForEnabledReasoning(t *testing.T) {
	p := vertexExpressTestProvider()
	for _, tc := range []struct {
		level modelcall.ThinkLevel
		want  bool
	}{
		{modelcall.ThinkUnset, true},
		{modelcall.ThinkOff, false},
		{modelcall.ThinkLow, true},
		{modelcall.ThinkMedium, true},
		{modelcall.ThinkHigh, true},
	} {
		req := modelcall.CompletionRequest{
			Model:    "gemini-2.5-flash",
			Think:    tc.level,
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		}
		cfg := p.Prepare(req).GenerationConfig
		if cfg == nil {
			t.Fatalf("level %v: generationConfig missing", tc.level)
		}
		if got := cfg.ThinkingConfig != nil; got != tc.want {
			t.Errorf("level %v: thinkingConfig present = %v, want %v", tc.level, got, tc.want)
		}
		if cfg.ThinkingConfig != nil {
			if !cfg.ThinkingConfig.IncludeThoughts {
				t.Errorf("level %v: includeThoughts must be on so reasoning stays out of content", tc.level)
			}
			if cfg.MaxOutputTokens <= *cfg.ThinkingConfig.ThinkingBudget {
				t.Errorf("level %v: maxOutputTokens %d must leave headroom above budget %d",
					tc.level, cfg.MaxOutputTokens, cfg.ThinkingConfig.ThinkingBudget)
			}
		}
	}
}

// A ResponseFormat asks for JSON via responseMimeType only: the provider's
// responseSchema is an OpenAPI subset, not the JSON Schema dialect the host
// carries, so the schema is not projected.
func TestVertexExpressResponseFormatSetsMIMETypeOnly(t *testing.T) {
	p := vertexExpressTestProvider()
	req := modelcall.CompletionRequest{
		Model:          "gemini-2.5-flash",
		Messages:       []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		ResponseFormat: modelcall.JSONSchemaFormat("thing", json.RawMessage(`{"type":"object"}`)),
	}
	cfg := p.Prepare(req).GenerationConfig
	if cfg.ResponseMIMEs != "application/json" {
		t.Fatalf("responseMimeType = %q, want application/json", cfg.ResponseMIMEs)
	}

	plain := p.Prepare(modelcall.CompletionRequest{
		Model:    "gemini-2.5-flash",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	}).GenerationConfig
	if plain.ResponseMIMEs != "" {
		t.Fatalf("responseMimeType = %q, want empty when no ResponseFormat is set", plain.ResponseMIMEs)
	}
}

// Per-model max_tokens is honored, and an orchestration turn is clamped.
func TestVertexExpressMaxOutputTokensRespectsCatalogAndTurnClass(t *testing.T) {
	p := vertexExpressTestProvider(modelinfo.Entry{ID: "gemini-2.5-flash", MaxTokens: 60000})

	open := p.Prepare(modelcall.CompletionRequest{
		Model:    "gemini-2.5-flash",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	if open.GenerationConfig.MaxOutputTokens != 60000 {
		t.Errorf("maxOutputTokens = %d, want the catalog value", open.GenerationConfig.MaxOutputTokens)
	}

	capped := p.Prepare(modelcall.CompletionRequest{
		Model:     "gemini-2.5-flash",
		Messages:  []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		MaxTokens: 512,
	})
	if capped.GenerationConfig.MaxOutputTokens != 512 {
		t.Errorf("maxOutputTokens = %d, want the request cap", capped.GenerationConfig.MaxOutputTokens)
	}
}

func TestVertexExpressCompleteMapsResponse(t *testing.T) {
	var gotBody Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(vertexExpressResponse{
			Candidates: []vertexExpressCandidate{{
				Content:      vertexExpressContent{Role: "model", Parts: []vertexExpressPart{{Text: "hello"}}},
				FinishReason: "STOP",
			}},
			UsageMetadata: &vertexExpressUsageMetadata{PromptTokenCount: 11, CandidatesTokenCount: 4},
		})
	}))
	defer srv.Close()

	p := New("vertex-express", srv.URL, "k", []modelinfo.Entry{{ID: "gemini-2.5-flash"}})
	got, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Model: "gemini-2.5-flash",
		Messages: []api.Message{
			{Role: api.MessageRoleSystem, Content: "be terse"},
			{Role: api.MessageRoleUser, Content: "hi"},
		},
	})
	testutil.FailErr(t, "Complete", err)

	if got.Content != "hello" {
		t.Errorf("content = %q", got.Content)
	}
	if got.Usage != (modelcall.TokenUsage{Present: true, PromptTokens: 11, CompletionTokens: 4}) {
		t.Errorf("usage = %+v", got.Usage)
	}
	if gotBody.SystemInstruction == nil || gotBody.SystemInstruction.Parts[0].Text != "be terse" {
		t.Errorf("systemInstruction = %+v, want the system message", gotBody.SystemInstruction)
	}
	if len(gotBody.Contents) != 1 || gotBody.Contents[0].Role != "user" {
		t.Errorf("contents = %+v, want the system message out of the turn list", gotBody.Contents)
	}
}

// A successful response can still report a safety block.
func TestVertexExpressCompleteSurfacesContentFilterBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model"},"finishReason":"SAFETY"}],"usageMetadata":{"promptTokenCount":9}}`))
	}))
	defer srv.Close()

	p := New("vertex-express", srv.URL, "k", []modelinfo.Entry{{ID: "gemini-2.5-flash"}})
	got, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Model:    "gemini-2.5-flash",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatalf("want an error, got completion %+v", got)
	}
	if !strings.Contains(err.Error(), "SAFETY") {
		t.Errorf("error = %q, want the finishReason named", err)
	}
}

// A 200 with no candidates at all is anomalous, not an empty answer.
func TestVertexExpressCompleteRejectsNoCandidates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"usageMetadata":{"promptTokenCount":9}}`))
	}))
	defer srv.Close()

	p := New("vertex-express", srv.URL, "k", []modelinfo.Entry{{ID: "gemini-2.5-flash"}})
	_, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Model:    "gemini-2.5-flash",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	empty, ok := failure.AsProviderEmptyCompletion(err)
	if !ok || !empty.Retryable {
		t.Fatalf("error = %v, want retryable empty completion", err)
	}
}

func TestVertexExpressCompleteDoesNotRetryPromptBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT","blockReasonMessage":"blocked"}}`))
	}))
	defer srv.Close()

	p := New("vertex-express", srv.URL, "k", []modelinfo.Entry{{ID: "gemini-2.5-flash"}})
	_, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Model:    "gemini-2.5-flash",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	empty, ok := failure.AsProviderEmptyCompletion(err)
	if !ok || empty.Retryable || empty.Reason != "PROHIBITED_CONTENT" {
		t.Fatalf("error = %+v, want non-retryable structured prompt block", err)
	}
}

// A normal answer that happens to hit the token cap is not an error.
func TestVertexExpressCompleteAllowsMaxTokensWithOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"partial ans"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":4}}`))
	}))
	defer srv.Close()

	p := New("vertex-express", srv.URL, "k", []modelinfo.Entry{{ID: "gemini-2.5-flash"}})
	got, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Model:    "gemini-2.5-flash",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	testutil.FailErr(t, "Complete", err)
	if got.Content != "partial ans" {
		t.Fatalf("content = %q", got.Content)
	}
}

// A Google API error body surfaces its message rather than the raw JSON.
func TestVertexExpressHTTPErrorUsesAPIMessage(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"object", `{"error":{"code":403,"message":"API key not valid","status":"PERMISSION_DENIED"}}`},
		{"array wrapped", `[{"error":{"code":403,"message":"API key not valid","status":"PERMISSION_DENIED"}}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := vertexExpressHTTPError(403, []byte(tc.body))
			if !strings.Contains(err.Error(), "API key not valid") {
				t.Fatalf("error = %q, want the API message", err)
			}
		})
	}
}

func TestVertexExpressHTTPErrorFallsBackToRawBody(t *testing.T) {
	err := vertexExpressHTTPError(502, []byte("upstream exploded"))
	if !strings.Contains(err.Error(), "upstream exploded") {
		t.Fatalf("error = %q, want the raw body when it is not a Google error envelope", err)
	}
}

func TestVertexExpressRateLimitedErrorType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"quota exceeded"}}`))
	}))
	defer srv.Close()

	p := New("vertex-express", srv.URL, "k", []modelinfo.Entry{{ID: "gemini-2.5-flash"}}).
		WithHTTPRetry(providerretry.ProviderHTTPRetry{MaxRetries: 0, Statuses: []int{429}})
	_, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Model:    "gemini-2.5-flash",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("want an error")
	}
	var rateLimited *failure.ProviderRateLimitedError
	if !errors.As(err, &rateLimited) {
		t.Fatalf("error type = %T, want *ProviderRateLimitedError", err)
	}
	if rateLimited.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", rateLimited.Status)
	}
}
