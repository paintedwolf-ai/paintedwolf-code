package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	anthropicprovider "github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCloudflareOutputLimitRecoversOnceAndPreservesUsage(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream_%t", stream), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
				}
				wantEffort := "high"
				if calls > 1 {
					wantEffort = "low"
				}
				if body["reasoning_effort"] != wantEffort {
					t.Errorf("wire effort = %v, want native high", body["reasoning_effort"])
				}
				if !stream {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"","reasoning_content":"thinking"},"finish_reason":"length"}],"usage":{"prompt_tokens":42,"completion_tokens":128}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n"+
					"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}],\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":1}}\n\n"+
					"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":42,\"completion_tokens\":128}}\n\n"+
					"data: [DONE]\n\n")
			}))
			defer server.Close()
			inner := openaicompat.NewCloudflare("cf-fixture", server.URL+"/accounts/account/ai/v1", nil, "fixture", nil, server.Client())
			provider := &responseRetryProvider{inner: inner, policy: retryEmptyOncePolicy()}
			req := modelcall.CompletionRequest{Model: "@cf/zai-org/glm-5.3-flash", Think: modelcall.ThinkHigh, Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}}}
			var completion *modelcall.Completion
			var err error
			if stream {
				var chunks <-chan modelcall.StreamChunk
				chunks, err = provider.Stream(t.Context(), req)
				if err != nil {
					t.Fatalf("start stream: %v", err)
				}
				completion, _, err = modelcall.CollectStream(chunks)
			} else {
				completion, err = provider.Complete(t.Context(), req)
			}
			empty, ok := failure.AsProviderEmptyCompletion(err)
			if !ok || empty.Retryable || empty.Reason != "length" || empty.Attempts != 2 || calls != 2 || !empty.RecoveryAttempted {
				t.Fatalf("output limit error = %v, requests = %d", err, calls)
			}
			if completion == nil || completion.Usage.PromptTokens != 84 || completion.Usage.CompletionTokens != 256 {
				t.Fatalf("failed completion lost billed usage: %+v", completion)
			}
		})
	}
}

func TestResponseRetryAddsUsageAcrossAttempts(t *testing.T) {
	for _, stream := range []bool{false, true} {
		inner := &responseRetryStub{
			complete: func(call int) (*modelcall.Completion, error) {
				out := &modelcall.Completion{Usage: modelcall.TokenUsage{PromptTokens: 10, CompletionTokens: call}}
				if call == 1 {
					return out, &failure.ProviderEmptyCompletionError{Retryable: true}
				}
				out.Content = "done"
				return out, nil
			},
			stream: func(call int) (<-chan modelcall.StreamChunk, error) {
				last := modelcall.StreamChunk{Usage: modelcall.TokenUsage{PromptTokens: 10, CompletionTokens: call}, Done: true}
				if call == 1 {
					last.Err = &failure.ProviderEmptyCompletionError{Retryable: true}
				} else {
					last.Content = "done"
				}
				return retryChunkStream(modelcall.StreamChunk{Usage: last.Usage}, last), nil
			},
		}
		provider := &responseRetryProvider{inner: inner, policy: retryEmptyOncePolicy()}
		var got *modelcall.Completion
		var err error
		if stream {
			var chunks <-chan modelcall.StreamChunk
			chunks, err = provider.Stream(t.Context(), modelcall.CompletionRequest{})
			if err != nil {
				t.Fatalf("start retry stream: %v", err)
			}
			got, _, err = modelcall.CollectStream(chunks)
		} else {
			got, err = provider.Complete(t.Context(), modelcall.CompletionRequest{})
		}
		if err != nil || got == nil || got.Usage.PromptTokens != 20 || got.Usage.CompletionTokens != 3 {
			t.Fatalf("stream=%v: combined usage = %+v, error = %v", stream, got, err)
		}
	}
}

func TestOutputLimitReasonsAcrossProtocols(t *testing.T) {
	for _, reason := range []string{"length", "max_tokens", "MAX_TOKENS"} {
		empty := &failure.ProviderEmptyCompletionError{Reason: reason, Terminal: true}
		if !empty.OutputLimitReached() {
			t.Fatalf("terminal output limit %q not recognized", reason)
		}
		empty.Terminal = false
		if empty.OutputLimitReached() {
			t.Fatalf("unterminated response claimed output limit %q", reason)
		}
	}
	if anthropicprovider.EmptyCompletionRetryable("max_tokens") || openaicompat.EmptyCompletionRetryable("length") {
		t.Fatal("output limit permits identical request replay")
	}
}
