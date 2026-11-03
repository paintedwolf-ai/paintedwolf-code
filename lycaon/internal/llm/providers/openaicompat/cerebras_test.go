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

	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCerebrasStreamingToolCallContinuationAndUsage(t *testing.T) {
	withThinkingRules(t, nil)
	const model = "gpt-oss-120b"
	var turn atomic.Int32
	var continuationReq map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer csk-secret" {
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
				`{"choices":[{"delta":{"role":"assistant","reasoning":"Inspect README.md before answering."}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_cb_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"README.md\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":140,"completion_tokens":32,"total_tokens":172,"prompt_tokens_details":{"cached_tokens":64}}}`,
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
			`{"choices":[{"delta":{"role":"assistant","reasoning":"Summarize the file contents."}}]}`,
			`{"choices":[{"delta":{"content":"The README documents the Cerebras setup."},"finish_reason":"stop"}],"usage":{"prompt_tokens":196,"completion_tokens":20,"total_tokens":216,"prompt_tokens_details":{"cached_tokens":128}}}`,
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

	p := New("cerebras", srv.URL+"/v1", "csk-secret", []modelinfo.Entry{
		{ID: model, Capabilities: modelinfo.ModelCapabilities{
			Chat:      modelinfo.Evidence(modelinfo.CapabilitySupported, "fixture"),
			Tools:     modelinfo.Evidence(modelinfo.CapabilitySupported, "fixture"),
			Reasoning: modelinfo.Evidence(modelinfo.CapabilitySupported, "fixture"),
		}},
	}).WithProfile(providerprofile.Cerebras()).
		WithReasoningWire(providerprofile.ReasoningWireContent).
		WithPromptCache(shippedPromptCache(t, "cerebras"))

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

	if first.Reasoning != "Inspect README.md before answering." {
		t.Fatalf("turn 1 reasoning = %q", first.Reasoning)
	}
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].Name != "read_file" || first.ToolCalls[0].Args["path"] != "README.md" {
		t.Fatalf("turn 1 tool calls = %+v", first.ToolCalls)
	}
	if !first.Usage.Present || first.Usage.PromptTokens != 140 || first.Usage.CompletionTokens != 32 || first.Usage.CacheReadInputTokens != 64 {
		t.Fatalf("turn 1 usage = %+v", first.Usage)
	}

	first.ProviderID, first.Model = "cerebras", model
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

	if second.Content != "The README documents the Cerebras setup." {
		t.Fatalf("turn 2 content = %q", second.Content)
	}
	if !second.Usage.Present || second.Usage.PromptTokens != 196 || second.Usage.CompletionTokens != 20 || second.Usage.CacheReadInputTokens != 128 {
		t.Fatalf("turn 2 usage = %+v", second.Usage)
	}

	msgs, ok := continuationReq["messages"].([]any)
	if !ok || len(msgs) != 3 {
		t.Fatalf("continuation messages = %#v", continuationReq["messages"])
	}
	assistantMsg, _ := msgs[1].(map[string]any)
	if assistantMsg["reasoning_content"] != "Inspect README.md before answering." {
		t.Fatalf("assistant reasoning_content = %#v", assistantMsg["reasoning_content"])
	}
	toolMsg, _ := msgs[2].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["content"] != "# Service\nRuns the worker." {
		t.Fatalf("tool message = %#v", toolMsg)
	}
}

func TestCerebrasStreamCancellation(t *testing.T) {
	t.Run("in-flight stream aborts upstream request and closes stream", func(t *testing.T) {
		serverAborted := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"partial"}}]}`+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			<-r.Context().Done()
			close(serverAborted)
		}))
		defer srv.Close()

		p := New("cerebras", srv.URL, "csk-secret", []modelinfo.Entry{{ID: "gpt-oss-120b"}}).
			WithProfile(providerprofile.Cerebras())

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		ch, err := p.Stream(ctx, modelcall.CompletionRequest{
			Model:    "gpt-oss-120b",
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
		})
		testutil.FailErr(t, "start stream", err)

		first, ok := <-ch
		if !ok || first.Content != "partial" {
			t.Fatalf("first chunk = %+v (ok=%v)", first, ok)
		}
		cancel()

		for chunk := range ch {
			if chunk.Err != nil && !errors.Is(chunk.Err, context.Canceled) {
				t.Fatalf("unexpected stream error after cancel: %v", chunk.Err)
			}
		}
		<-serverAborted
	})

	t.Run("pre-header cancellation returns context.Canceled without retry", func(t *testing.T) {
		var attempts atomic.Int32
		inHandler := make(chan struct{})
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			if attempts.Add(1) == 1 {
				close(inHandler)
			}
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}))
		defer func() {
			close(release)
			srv.Close()
		}()

		p := New("cerebras", srv.URL, "csk-secret", []modelinfo.Entry{{ID: "gpt-oss-120b"}}).
			WithProfile(providerprofile.Cerebras()).
			WithHTTPRetry(providerretry.ProviderHTTPRetry{
				MaxRetries: 2,
				MaxWaitMs:  100,
				BackoffMs:  []int{1, 2},
				Statuses:   []int{http.StatusTooManyRequests},
			})

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		errCh := make(chan error, 1)
		go func() {
			_, err := p.Stream(ctx, modelcall.CompletionRequest{
				Model:    "gpt-oss-120b",
				Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
			})
			errCh <- err
		}()

		<-inHandler
		cancel()

		err := <-errCh
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stream error after cancel = %v, want context.Canceled", err)
		}
		if attempts.Load() != 1 {
			t.Fatalf("attempts = %d, want 1", attempts.Load())
		}
	})
}

func TestCerebrasDiscoveryAndErrorRecovery(t *testing.T) {
	t.Run("discovery lists account models and leaves unknown capabilities unknown", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/models" {
				http.NotFound(w, r)
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer csk-key" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-oss-120b","object":"model","owned_by":"OpenAI"},{"id":"llama3.1-8b","object":"model","owned_by":"Meta"}]}`))
		}))
		defer srv.Close()

		entries, err := discovery.FromProfile(t.Context(), srv.URL+"/v1", "csk-key", srv.Client(), providerprofile.Cerebras().DiscoveryProfile)
		testutil.FailErr(t, "discover cerebras models", err)
		if len(entries) != 2 || entries[0].ID != "gpt-oss-120b" || entries[1].ID != "llama3.1-8b" {
			t.Fatalf("entries = %+v", entries)
		}
		for _, entry := range entries {
			if entry.Untyped {
				t.Fatalf("%s must be typed", entry.ID)
			}
			if !modelinfo.Supported(entry.Capabilities.Chat) || !modelinfo.Supported(entry.Callable) {
				t.Fatalf("%s chat/callable = %+v / %+v", entry.ID, entry.Capabilities.Chat, entry.Callable)
			}
			if entry.Capabilities.Tools.State != "" || entry.Capabilities.Reasoning.State != "" || entry.Capabilities.Streaming.State != "" {
				t.Fatalf("%s invented unadvertised capability: %+v", entry.ID, entry.Capabilities)
			}
		}
	})

	t.Run("flat 404 model_not_found classifies as ModelRefusedError", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Model qwen-3-235b-a22b-instruct-2507 does not exist or you do not have access to it.","type":"not_found_error","param":"model","code":"model_not_found"}`))
		}))
		defer srv.Close()

		p := New("cerebras", srv.URL, "csk-key", []modelinfo.Entry{{ID: "qwen-3-235b-a22b-instruct-2507"}}).
			WithProfile(providerprofile.Cerebras())
		_, err := p.Complete(t.Context(), modelcall.CompletionRequest{
			Model:    "qwen-3-235b-a22b-instruct-2507",
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		})
		refused, ok := providerretry.AsModelRefused(err)
		if !ok || refused.Evidence != providerretry.RefusalEvidenceModelIdentity {
			t.Fatalf("expected ModelRefusedError(model-identity), got %T (%v)", err, err)
		}
	})

	t.Run("authentication error is actionable and terminal", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Wrong API Key","type":"invalid_request_error","param":"authorization","code":"wrong_api_key"}`))
		}))
		defer srv.Close()

		p := New("cerebras", srv.URL, "bad-key", []modelinfo.Entry{{ID: "gpt-oss-120b"}}).
			WithProfile(providerprofile.Cerebras())
		_, err := p.Complete(t.Context(), modelcall.CompletionRequest{
			Model:    "gpt-oss-120b",
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		})
		if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Wrong API Key") {
			t.Fatalf("expected actionable 401 error, got %v", err)
		}
	})

	t.Run("rate limit retries and recovers", func(t *testing.T) {
		var attempts atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n := attempts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if n == 1 {
				w.Header().Set("retry-after-ms", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"message":"Requests per minute limit exceeded","type":"too_many_requests_error","code":"rate_limit_exceeded"}`))
				return
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`))
		}))
		defer srv.Close()

		p := New("cerebras", srv.URL, "csk-key", []modelinfo.Entry{{ID: "gpt-oss-120b"}}).
			WithProfile(providerprofile.Cerebras()).
			WithHTTPRetry(providerretry.ProviderHTTPRetry{
				MaxRetries:  2,
				MaxWaitMs:   1000,
				BackoffMs:   []int{1, 2},
				Statuses:    []int{http.StatusTooManyRequests},
				WaitHeaders: []string{"Retry-After", "retry-after-ms"},
			})

		out, err := p.Complete(t.Context(), modelcall.CompletionRequest{
			Model:    "gpt-oss-120b",
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
			_, _ = w.Write([]byte(`{"message":"Server is busy, please try again later.","type":"service_unavailable_error","code":"service_unavailable"}`))
		}))
		defer srv.Close()

		p := New("cerebras", srv.URL, "csk-key", []modelinfo.Entry{{ID: "gpt-oss-120b"}}).
			WithProfile(providerprofile.Cerebras()).
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
			Model:    "gpt-oss-120b",
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		})
		var overloaded *failure.ProviderOverloadedError
		if !errors.As(err, &overloaded) {
			t.Fatalf("expected ProviderOverloadedError, got %T (%v)", err, err)
		}
	})
}
