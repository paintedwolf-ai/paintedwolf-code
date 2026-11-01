package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestScalewayStreamToolCallContinuationAndUsage(t *testing.T) {
	withThinkingRules(t, nil)

	var (
		step    atomic.Int32
		gotAuth string
		bodies  [][]byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		bodies = append(bodies, raw)

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		if step.Add(1) == 1 {
			frames := []string{
				`{"choices":[{"delta":{"role":"assistant","reasoning_content":"Check repository layout first."}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_scw_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"go.mod\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":120,"completion_tokens":28,"prompt_tokens_details":{"cached_tokens":64}}}`,
			}
			for _, f := range frames {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", f)
				if flusher != nil {
					flusher.Flush()
				}
			}
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"Module is ready.\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":160,\"completion_tokens\":12,\"prompt_tokens_details\":{\"cached_tokens\":120}}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	models := []modelinfo.Entry{{
		ID:            "qwen3-235b-a22b-instruct-2507",
		ContextLength: 128000,
	}}
	p := New("scaleway", srv.URL+"/v1", "scw-secret-token", models).
		WithProfile(providerprofile.Scaleway()).
		WithPromptCache(shippedPromptCache(t, "scaleway"))

	req := modelcall.CompletionRequest{
		Model: "qwen3-235b-a22b-instruct-2507",
		Tools: []tools.ToolMeta{{Name: "read_file"}},
		Messages: []api.Message{{
			Role:    api.MessageRoleUser,
			Content: "Read go.mod",
		}},
	}
	ch, err := p.Stream(context.Background(), req)
	testutil.FailErr(t, "Stream initial turn", err)
	first, _, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "CollectStream initial turn", err)

	if gotAuth != "Bearer scw-secret-token" {
		t.Fatalf("Authorization = %q, want Bearer scw-secret-token", gotAuth)
	}
	if first.Reasoning != "Check repository layout first." {
		t.Fatalf("Reasoning = %q", first.Reasoning)
	}
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].Name != "read_file" || first.ToolCalls[0].Args["path"] != "go.mod" {
		t.Fatalf("ToolCalls = %+v", first.ToolCalls)
	}
	usage := first.Usage
	if usage.PromptTokens != 120 || usage.CompletionTokens != 28 || usage.CacheReadInputTokens != 64 {
		t.Fatalf("usage = %+v", usage)
	}

	first.ProviderID = "scaleway"
	first.Model = req.Model
	req.Messages = append(
		req.Messages,
		api.Message{
			Role:           api.MessageRoleAssistant,
			ToolCalls:      first.ToolCalls,
			ModelReasoning: first.ModelReasoning(),
		},
		api.Message{
			Role: api.MessageRoleTool,
			ToolResult: &api.ToolResult{
				ToolCallID: first.ToolCalls[0].ID,
				Content:    "module example.com/test",
			},
		},
	)
	ch, err = p.Stream(context.Background(), req)
	testutil.FailErr(t, "Stream continuation turn", err)
	second, _, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "CollectStream continuation turn", err)
	if second.Content != "Module is ready." {
		t.Fatalf("second content = %q", second.Content)
	}
	secondUsage := second.Usage
	if secondUsage.CacheReadInputTokens != 120 {
		t.Fatalf("second usage = %+v", secondUsage)
	}
	if len(bodies) != 2 {
		t.Fatalf("captured requests = %d, want 2", len(bodies))
	}

	var wire struct {
		Messages []Message `json:"messages"`
	}
	testutil.FailErr(t, "unmarshal continuation body", json.Unmarshal(bodies[1], &wire))
	if len(wire.Messages) != 3 {
		t.Fatalf("continuation messages = %d, want 3", len(wire.Messages))
	}
	if wire.Messages[1].ReasoningContent != "Check repository layout first." {
		t.Fatalf("replayed reasoning_content = %q", wire.Messages[1].ReasoningContent)
	}
	if len(wire.Messages[1].ToolCalls) != 1 || wire.Messages[2].ToolCallID != wire.Messages[1].ToolCalls[0].ID {
		t.Fatalf("tool call pairing mismatch: assistant=%+v tool=%+v", wire.Messages[1].ToolCalls, wire.Messages[2])
	}
}

func TestScalewayStreamCancellationAndFaultClassification(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"}}]}\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			cancel()
			<-r.Context().Done()
		}))
		defer srv.Close()

		p := New("scaleway", srv.URL+"/v1", "scw-secret", nil).WithProfile(providerprofile.Scaleway())
		ch, err := p.Stream(ctx, modelcall.CompletionRequest{
			Model:    "qwen3-235b-a22b-instruct-2507",
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
		})
		if err == nil {
			_, _, err = modelcall.CollectStream(ch)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})

	t.Run("auth 401 rejected without retry", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid secret key","type":"authentication_error"}}`))
		}))
		defer srv.Close()

		p := New("scaleway", srv.URL+"/v1", "bad-key", nil).
			WithProfile(providerprofile.Scaleway()).
			WithHTTPRetry(testRetryPolicy())
		_, err := p.Complete(context.Background(), modelcall.CompletionRequest{
			Model:    "qwen3-235b-a22b-instruct-2507",
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
		})
		rejected, ok := providerretry.AsProviderRequestRejected(err)
		if !ok || rejected.Status != http.StatusUnauthorized || rejected.ProviderID != "scaleway" {
			t.Fatalf("err = %v, want 401 ProviderRequestRejectedError", err)
		}
		if hits.Load() != 1 {
			t.Fatalf("hits = %d, want 1", hits.Load())
		}
	})

	t.Run("rate limit 429 and overload 503", func(t *testing.T) {
		var mode atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "0")
			if mode.Load() == 0 {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"quota exceeded"}}`))
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"capacity unavailable"}}`))
		}))
		defer srv.Close()

		p := New("scaleway", srv.URL+"/v1", "scw-key", nil).
			WithProfile(providerprofile.Scaleway()).
			WithHTTPRetry(providerretry.ProviderHTTPRetry{
				MaxRetries:  1,
				MaxWaitMs:   50,
				BackoffMs:   []int{1},
				Statuses:    []int{429},
				WaitHeaders: []string{"Retry-After"},
				Capacity: &providerretry.ProviderHTTPRetry{
					MaxRetries:  1,
					MaxWaitMs:   50,
					BackoffMs:   []int{1},
					Statuses:    []int{503},
					WaitHeaders: []string{"Retry-After"},
				},
			})
		_, err := p.Complete(context.Background(), modelcall.CompletionRequest{
			Model:    "qwen3-235b-a22b-instruct-2507",
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
		})
		if _, ok := failure.AsProviderRateLimited(err); !ok {
			t.Fatalf("err = %v, want ProviderRateLimitedError", err)
		}

		mode.Store(1)
		_, err = p.Complete(context.Background(), modelcall.CompletionRequest{
			Model:    "qwen3-235b-a22b-instruct-2507",
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
		})
		if _, ok := failure.AsProviderOverloaded(err); !ok {
			t.Fatalf("err = %v, want ProviderOverloadedError", err)
		}
	})
}
