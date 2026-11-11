package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDeepInfraStreamingToolCallContinuationAndUsage(t *testing.T) {
	withThinkingRules(t, nil)
	const model = "deepseek-ai/DeepSeek-V3.2"
	var turn atomic.Int32
	var continuationReq map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/openai/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer di-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		if turn.Add(1) == 1 {
			chunks := []string{
				`{"choices":[{"delta":{"role":"assistant","reasoning_content":"Check the workspace files first."}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_di_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"README.md\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":120,"completion_tokens":35,"total_tokens":155,"prompt_tokens_details":{"cached_tokens":64}}}`,
			}
			for _, c := range chunks {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", c)
				if flusher != nil {
					flusher.Flush()
				}
			}
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}

		continuationReq = body
		chunks := []string{
			`{"choices":[{"delta":{"role":"assistant","reasoning_content":"Summarize README contents."}}]}`,
			`{"choices":[{"delta":{"content":"The README describes the service."},"finish_reason":"stop"}],"usage":{"prompt_tokens":180,"completion_tokens":18,"total_tokens":198,"prompt_tokens_details":{"cached_tokens":120}}}`,
		}
		for _, c := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", c)
			if flusher != nil {
				flusher.Flush()
			}
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := New("deepinfra", srv.URL+"/v1/openai", "di-secret", []modelinfo.Entry{
		{ID: model, Capabilities: modelinfo.ModelCapabilities{
			Chat:      modelinfo.Evidence(modelinfo.CapabilitySupported, "fixture"),
			Tools:     modelinfo.Evidence(modelinfo.CapabilitySupported, "fixture"),
			Reasoning: modelinfo.Evidence(modelinfo.CapabilitySupported, "fixture"),
		}},
	}).WithProfile(providerprofile.DeepInfra()).WithPromptCache(shippedPromptCache(t, "deepinfra"))

	req := modelcall.CompletionRequest{
		Model: model,
		Tools: []tools.ToolMeta{{
			Name:        "read_file",
			Description: "Read a file",
			ArgsSchema:  map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
		}},
		Messages: []api.Message{
			{Role: api.MessageRoleUser, Content: "What is in README.md?"},
		},
	}

	ch, err := p.Stream(t.Context(), req)
	testutil.FailErr(t, "start stream turn 1", err)
	first, _, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "collect stream turn 1", err)

	if first.Reasoning != "Check the workspace files first." {
		t.Fatalf("turn 1 reasoning = %q", first.Reasoning)
	}
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].Name != "read_file" || first.ToolCalls[0].Args["path"] != "README.md" {
		t.Fatalf("turn 1 tool calls = %+v", first.ToolCalls)
	}
	if first.Usage.PromptTokens != 120 || first.Usage.CompletionTokens != 35 || first.Usage.CacheReadInputTokens != 64 {
		t.Fatalf("turn 1 usage = %+v", first.Usage)
	}

	first.ProviderID, first.Model = "deepinfra", model
	req.Messages = append(req.Messages,
		api.Message{
			Role:           api.MessageRoleAssistant,
			ToolCalls:      first.ToolCalls,
			ModelReasoning: first.ModelReasoning(),
		},
		api.Message{
			Role: api.MessageRoleTool,
			ToolResult: &api.ToolResult{
				ToolCallID: first.ToolCalls[0].ID,
				Tool:       "read_file",
				Content:    "# Service\nRuns the worker.",
			},
		},
	)

	ch, err = p.Stream(t.Context(), req)
	testutil.FailErr(t, "start stream turn 2", err)
	second, _, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "collect stream turn 2", err)

	if second.Content != "The README describes the service." {
		t.Fatalf("turn 2 content = %q", second.Content)
	}
	if second.Usage.PromptTokens != 180 || second.Usage.CompletionTokens != 18 || second.Usage.CacheReadInputTokens != 120 {
		t.Fatalf("turn 2 usage = %+v", second.Usage)
	}

	msgs, ok := continuationReq["messages"].([]any)
	if !ok || len(msgs) != 3 {
		t.Fatalf("continuation messages = %#v", continuationReq["messages"])
	}
	assistantMsg, _ := msgs[1].(map[string]any)
	if assistantMsg["reasoning_content"] != "Check the workspace files first." {
		t.Fatalf("assistant reasoning_content = %#v", assistantMsg["reasoning_content"])
	}
	toolMsg, _ := msgs[2].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["content"] != "# Service\nRuns the worker." {
		t.Fatalf("tool message = %#v", toolMsg)
	}
}

func TestDeepInfraStreamCancellation(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"partial"}}]}`+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	p := New("deepinfra", srv.URL, "di-secret", []modelinfo.Entry{{ID: "deepseek-ai/DeepSeek-V3.2"}}).
		WithProfile(providerprofile.DeepInfra())

	req := modelcall.CompletionRequest{
		Model:    "deepseek-ai/DeepSeek-V3.2",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
	}

	streamCtx, cancelStream := context.WithCancel(t.Context())
	defer cancelStream()

	ch, err := p.Stream(streamCtx, req)
	testutil.FailErr(t, "start stream", err)

	firstChunk, ok := <-ch
	if !ok || firstChunk.Content != "partial" {
		t.Fatalf("first chunk = %+v (ok=%v), want partial content", firstChunk, ok)
	}
	cancelStream()
	for range ch {
	}
	if attempts.Load() != 1 {
		t.Fatalf("stream attempts after cancel = %d, want 1", attempts.Load())
	}

	completeCtx, cancelComplete := context.WithCancel(t.Context())
	cancelComplete()
	_, err = p.Complete(completeCtx, req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("complete error after cancel = %v, want context.Canceled", err)
	}
}

func TestDeepInfraErrorClassificationAndRetryRecovery(t *testing.T) {
	t.Run("authentication error is terminal", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid token","type":"invalid_request_error"}}`))
		}))
		defer srv.Close()

		p := New("deepinfra", srv.URL, "bad-key", []modelinfo.Entry{{ID: "deepseek-ai/DeepSeek-V3.2"}}).
			WithProfile(providerprofile.DeepInfra())
		_, err := p.Complete(t.Context(), modelcall.CompletionRequest{
			Model:    "deepseek-ai/DeepSeek-V3.2",
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		})
		if err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("expected 401 auth error, got %v", err)
		}
	})

	t.Run("rate limit and overload retry then recover", func(t *testing.T) {
		var attempts atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n := attempts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if n == 1 {
				w.Header().Set("retry-after-ms", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"Rate limit exceeded"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`))
		}))
		defer srv.Close()

		p := New("deepinfra", srv.URL, "di-key", []modelinfo.Entry{{ID: "deepseek-ai/DeepSeek-V3.2"}}).
			WithProfile(providerprofile.DeepInfra()).
			WithHTTPRetry(providerretry.ProviderHTTPRetry{
				MaxRetries:  2,
				MaxWaitMs:   1000,
				BackoffMs:   []int{1, 2},
				Statuses:    []int{http.StatusTooManyRequests},
				WaitHeaders: []string{"Retry-After", "retry-after-ms"},
			})

		out, err := p.Complete(t.Context(), modelcall.CompletionRequest{
			Model:    "deepseek-ai/DeepSeek-V3.2",
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		})
		testutil.FailErr(t, "complete after retry", err)
		if out.Content != "recovered" || attempts.Load() != 2 {
			t.Fatalf("content=%q attempts=%d", out.Content, attempts.Load())
		}
	})

	t.Run("exhausted overload returns ProviderOverloadedError", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"Model overloaded"}}`))
		}))
		defer srv.Close()

		p := New("deepinfra", srv.URL, "di-key", []modelinfo.Entry{{ID: "deepseek-ai/DeepSeek-V3.2"}}).
			WithProfile(providerprofile.DeepInfra()).
			WithHTTPRetry(providerretry.ProviderHTTPRetry{
				MaxRetries: 0,
				MaxWaitMs:  100,
				Statuses:   []int{http.StatusTooManyRequests},
				Capacity: &providerretry.ProviderHTTPRetry{
					MaxRetries: 0,
					MaxWaitMs:  100,
					Statuses:   []int{http.StatusServiceUnavailable},
				},
			})

		_, err := p.Complete(t.Context(), modelcall.CompletionRequest{
			Model:    "deepseek-ai/DeepSeek-V3.2",
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		})
		var overloaded *failure.ProviderOverloadedError
		if !errors.As(err, &overloaded) {
			t.Fatalf("expected ProviderOverloadedError, got %T (%v)", err, err)
		}
	})
}
