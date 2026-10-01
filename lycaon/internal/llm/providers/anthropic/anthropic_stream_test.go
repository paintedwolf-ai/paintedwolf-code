package anthropic

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// anthropicSSEServer emits complete fixture frames.
func anthropicSSEServer(t *testing.T, events []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("expected flusher")
			return
		}
		for _, ev := range events {
			fmt.Fprintf(w, "%s\n\n", ev)
			flusher.Flush()
		}
	}))
}

func TestAnthropicStreamHappyPath(t *testing.T) {
	server := anthropicSSEServer(t, []string{
		`event: message_start
data: {"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","content":[],"usage":{"input_tokens":12,"output_tokens":1,"cache_creation_input_tokens":3,"cache_read_input_tokens":7}}}`,
		`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`,
		`event: content_block_stop
data: {"type":"content_block_stop","index":0}`,
		`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`,
		`event: message_stop
data: {"type":"message_stop"}`,
	})
	defer server.Close()

	provider := New("test", server.URL, "test-key", []modelinfo.Entry{{ID: "claude-test"}})
	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Hello"}},
	})
	testutil.FailErr(t, "provider.Stream failed", err)
	completion, tokens, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "CollectStream failed", err)

	if completion.Content != "Hello world" {
		t.Fatalf("content = %q", completion.Content)
	}
	if len(tokens) != 2 || tokens[0] != "Hello" || tokens[1] != " world" {
		t.Fatalf("tokens = %v", tokens)
	}
	// PromptTokens is the inclusive input total: the wire's input_tokens (12)
	// plus both cache buckets (7 read + 3 creation).
	want := modelcall.TokenUsage{
		Present:                  true,
		PromptTokens:             22,
		CompletionTokens:         9,
		CacheReadInputTokens:     7,
		CacheCreationInputTokens: 3,
	}
	if completion.Usage != want {
		t.Fatalf("usage = %+v, want %+v", completion.Usage, want)
	}
}

func TestAnthropicStreamRejectsMissingTerminalEvent(t *testing.T) {
	server := anthropicSSEServer(t, []string{
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
	})
	defer server.Close()
	provider := New("test", server.URL, "test-key", []modelinfo.Entry{{ID: "claude-test"}})
	ch, err := provider.Stream(t.Context(), modelcall.CompletionRequest{})
	testutil.FailErr(t, "start incomplete stream", err)
	if _, _, err := modelcall.CollectStream(ch); err == nil || !strings.Contains(err.Error(), "message_stop") {
		t.Fatalf("error = %v, want missing message_stop", err)
	}
}

func TestAnthropicInterruptedStreamRetainsObservedCacheUsage(t *testing.T) {
	server := anthropicSSEServer(t, []string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":400}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
	})
	defer server.Close()
	provider := New("test", server.URL, "test-key", []modelinfo.Entry{{ID: "claude-test"}})
	ch, err := provider.Stream(t.Context(), modelcall.CompletionRequest{})
	testutil.FailErr(t, "start interrupted stream", err)
	completion, _, err := modelcall.CollectStream(ch)
	if err == nil {
		t.Fatal("interrupted stream completed successfully")
	}
	if completion.Usage.PromptTokens != 500 || completion.Usage.CacheReadInputTokens != 400 || !completion.Usage.Incomplete {
		t.Fatalf("lost observed partial usage: %+v", completion.Usage)
	}
}

func TestAnthropicStreamThinkingDeltaRoutedAsReasoning(t *testing.T) {
	server := anthropicSSEServer(t, []string{
		`event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}`,
		`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"weigh "}}`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"options"}}`,
		`event: content_block_stop
data: {"type":"content_block_stop","index":0}`,
		`event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}`,
		`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
		`event: message_stop
data: {"type":"message_stop"}`,
	})
	defer server.Close()

	provider := New("test", server.URL, "test-key", []modelinfo.Entry{{ID: "claude-test"}})
	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "decide"}},
	})
	testutil.FailErr(t, "provider.Stream failed", err)
	completion, _, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "CollectStream failed", err)

	if completion.Content != "answer" {
		t.Fatalf("content = %q, thinking leaked into content?", completion.Content)
	}
	if completion.Reasoning != "weigh options" {
		t.Fatalf("reasoning = %q, want %q", completion.Reasoning, "weigh options")
	}
}

func TestAnthropicStreamToolUseAccumulated(t *testing.T) {
	server := anthropicSSEServer(t, []string{
		`event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":8}}}`,
		`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_abc","name":"read","input":{}}}`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"a.txt\"}"}}`,
		`event: content_block_stop
data: {"type":"content_block_stop","index":0}`,
		`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":6}}`,
		`event: message_stop
data: {"type":"message_stop"}`,
	})
	defer server.Close()

	provider := New("test", server.URL, "test-key", []modelinfo.Entry{{ID: "claude-test"}})
	if provider.Profile().ToolCallID != providerprofile.ToolCallIDWireRoundTrip {
		t.Fatalf("anthropic driver profile ToolCallID = %q, want %q",
			provider.Profile().ToolCallID, providerprofile.ToolCallIDWireRoundTrip)
	}
	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "read"}},
	})
	testutil.FailErr(t, "provider.Stream failed", err)
	completion, _, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "CollectStream failed", err)

	if len(completion.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v", completion.ToolCalls)
	}
	tc := completion.ToolCalls[0]
	// ToolCallIDWireRoundTrip: the provider's own tool_use id must survive as
	// WireID so tool results can echo it back verbatim; identity is host-minted.
	if tc.WireID != "toolu_abc" || tc.Name != "read" {
		t.Fatalf("tool call = %+v", tc)
	}
	if tc.ID == "" || tc.ID == "toolu_abc" {
		t.Fatalf("expected host-minted id distinct from wire token, got %q", tc.ID)
	}
	if tc.Args["path"] != "a.txt" {
		t.Fatalf("args = %+v", tc.Args)
	}
	if tc.ArgsTruncated || tc.ArgsMalformed {
		t.Fatalf("complete args flagged: truncated=%v malformed=%v", tc.ArgsTruncated, tc.ArgsMalformed)
	}
	if completion.Usage.PromptTokens != 8 || completion.Usage.CompletionTokens != 6 {
		t.Fatalf("usage = %+v", completion.Usage)
	}
}

func TestAnthropicStreamEmitsNamedToolProgressOnBlockStart(t *testing.T) {
	server := anthropicSSEServer(t, []string{
		`event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":8}}}`,
		`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_abc","name":"command","input":{}}}`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`,
		`event: content_block_stop
data: {"type":"content_block_stop","index":0}`,
		`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":6}}`,
		`event: message_stop
data: {"type":"message_stop"}`,
	})
	defer server.Close()

	provider := New("test", server.URL, "test-key", []modelinfo.Entry{{ID: "claude-test"}})
	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "run"}},
	})
	testutil.FailErr(t, "provider.Stream failed", err)

	var firstName, firstID string
	completion, _, err := modelcall.CollectStreamWithProgress(ch, func(c *modelcall.Completion) {
		if c == nil || len(c.ToolCalls) == 0 || firstName != "" {
			return
		}
		firstName = c.ToolCalls[0].Name
		firstID = c.ToolCalls[0].ID
	})
	testutil.FailErr(t, "CollectStreamWithProgress failed", err)
	if firstName != "command" || firstID == "" || firstID == "toolu_abc" {
		t.Fatalf("first progress tool_call = name=%q id=%q want named command with host id", firstName, firstID)
	}
	if len(completion.ToolCalls) != 1 || completion.ToolCalls[0].Name != "command" {
		t.Fatalf("settled tool_calls = %+v", completion.ToolCalls)
	}
}

func TestAnthropicStreamMaxTokensFlagsTruncatedToolArgs(t *testing.T) {
	// stop_reason "max_tokens" with unterminated tool-args JSON must surface as
	// ArgsTruncated (not ArgsMalformed) on the terminal chunk.
	server := anthropicSSEServer(t, []string{
		`event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":8}}}`,
		`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_cut","name":"write","input":{}}}`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a.txt\",\"content\":\"trunc"}}`,
		`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":99}}`,
		`event: message_stop
data: {"type":"message_stop"}`,
	})
	defer server.Close()

	provider := New("test", server.URL, "test-key", []modelinfo.Entry{{ID: "claude-test"}})
	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "write"}},
	})
	testutil.FailErr(t, "provider.Stream failed", err)
	completion, _, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "CollectStream failed", err)

	if len(completion.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v", completion.ToolCalls)
	}
	tc := completion.ToolCalls[0]
	if !tc.ArgsTruncated {
		t.Fatalf("expected ArgsTruncated on max_tokens stop, got %+v", tc)
	}
	if tc.ArgsMalformed {
		t.Fatalf("length-capped args must not be flagged malformed: %+v", tc)
	}
	if tc.WireID != "toolu_cut" || tc.Name != "write" {
		t.Fatalf("tool call = %+v", tc)
	}
}

func TestAnthropicCompleteSendsAuthHeaders(t *testing.T) {
	var gotAPIKey, gotVersion, gotAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		gotAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"Hi"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}}`))
	}))
	defer server.Close()

	provider := New("test", server.URL, "secret-key", []modelinfo.Entry{{ID: "claude-test"}})
	completion, err := provider.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Hello"}},
	})
	testutil.FailErr(t, "provider.Complete failed", err)

	if gotAPIKey != "secret-key" {
		t.Fatalf("x-api-key = %q", gotAPIKey)
	}
	if gotVersion != discovery.AnthropicVersion {
		t.Fatalf("anthropic-version = %q, want %q", gotVersion, discovery.AnthropicVersion)
	}
	if gotAuthorization != "" {
		t.Fatalf("Authorization must not be sent on the native Anthropic wire, got %q", gotAuthorization)
	}
	if completion.Content != "Hi" {
		t.Fatalf("content = %q", completion.Content)
	}
	if completion.Usage.PromptTokens != 4 || completion.Usage.CompletionTokens != 2 {
		t.Fatalf("usage = %+v", completion.Usage)
	}
}

func anthropicTestRetryPolicy(maxRetries int) providerretry.ProviderHTTPRetry {
	return providerretry.ProviderHTTPRetry{
		MaxRetries:  maxRetries,
		MaxWaitMs:   1000,
		BackoffMs:   []int{1, 1, 1},
		Statuses:    []int{429},
		WaitHeaders: []string{"retry-after"},
	}
}

func TestAnthropicRetryAfterThenSuccess(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	provider := New("test", server.URL, "test-key", []modelinfo.Entry{{ID: "claude-test"}}).
		WithHTTPRetry(anthropicTestRetryPolicy(2))
	completion, err := provider.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Hello"}},
	})
	testutil.FailErr(t, "provider.Complete after retry failed", err)

	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	if completion.Content != "ok" {
		t.Fatalf("content = %q", completion.Content)
	}
}

func TestAnthropicRetryExhaustedRateLimitedError(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"overloaded"}}`))
	}))
	defer server.Close()

	provider := New("test", server.URL, "test-key", []modelinfo.Entry{{ID: "claude-test"}}).
		WithHTTPRetry(anthropicTestRetryPolicy(1))
	_, err := provider.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Hello"}},
	})
	if err == nil {
		t.Fatal("expected error after retry exhaustion")
	}
	// MaxRetries=1 means the original attempt plus one retry.
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	rl, ok := failure.AsProviderRateLimited(err)
	if !ok {
		t.Fatalf("expected ProviderRateLimitedError, got %T: %v", err, err)
	}
	if rl.ProviderID != "test" || rl.Model != "claude-test" ||
		rl.Status != http.StatusTooManyRequests || rl.Attempts != 2 {
		t.Fatalf("rate-limited error = %+v", rl)
	}
}
