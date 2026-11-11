package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
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
			truncated, ok := failure.AsProviderOutputTruncated(err)
			if !ok || truncated.FinishReason != "length" || truncated.Attempts != 2 || calls != 2 || !truncated.RecoveryAttempted {
				t.Fatalf("output limit error = %v, requests = %d", err, calls)
			}
			if _, isEmpty := failure.AsProviderEmptyCompletion(err); isEmpty {
				t.Fatalf("length output limit misclassified as empty completion: %v", err)
			}
			if completion.ContextInputTokens() != 42 {
				t.Fatalf("failed attempts inflated context input: %d", completion.ContextInputTokens())
			}
			if completion == nil || completion.Usage.PromptTokens != 84 || completion.Usage.CompletionTokens != 256 {
				t.Fatalf("failed completion lost billed usage: %+v", completion)
			}
		})
	}
}

func TestResponseRetryAddsUsageAcrossAttempts(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, finalReported := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream_%t_reported_%t", stream, finalReported), func(t *testing.T) {
				attemptUsage := func(call int) modelcall.TokenUsage {
					if call == 2 && !finalReported {
						return modelcall.TokenUsage{}
					}
					return modelcall.TokenUsage{PromptTokens: 10, CompletionTokens: call, CacheReadInputTokens: 4}
				}
				inner := &responseRetryStub{
					complete: func(call int) (*modelcall.Completion, error) {
						out := &modelcall.Completion{Usage: attemptUsage(call)}
						if call == 1 {
							return out, &failure.ProviderEmptyCompletionError{Retryable: true}
						}
						out.Content = "done"
						return out, nil
					},
					stream: func(call int) (<-chan modelcall.StreamChunk, error) {
						last := modelcall.StreamChunk{Usage: attemptUsage(call), Done: true}
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
				want := modelcall.TokenUsage{PromptTokens: 10, CompletionTokens: 1, CacheReadInputTokens: 4}
				wantContext := 0
				if finalReported {
					want.PromptTokens, want.CompletionTokens, want.CacheReadInputTokens = 20, 3, 8
					wantContext = 10
				}
				if err != nil || got == nil || got.Usage.PromptTokens != want.PromptTokens || got.Usage.CompletionTokens != want.CompletionTokens || got.Usage.CacheReadInputTokens != want.CacheReadInputTokens {
					t.Fatalf("combined billing usage = %+v, error = %v", got, err)
				}
				if got.ContextInputTokens() != wantContext {
					t.Fatalf("context input = %d, want %d independently of billing", got.ContextInputTokens(), wantContext)
				}
			})
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

func TestResponseRetryPreservesAttemptMetadata(t *testing.T) {
	for _, stream := range []bool{false, true} {
		billed := modelcall.TokenUsage{PromptTokens: 20, CompletionTokens: 3, CacheReadInputTokens: 8}
		attempt := modelcall.TokenUsage{PromptTokens: 7, CompletionTokens: 2, CacheReadInputTokens: 4}
		inner := &responseRetryStub{
			complete: func(int) (*modelcall.Completion, error) {
				return &modelcall.Completion{Content: "done", Usage: billed, AttemptUsage: &attempt}, nil
			},
			stream: func(int) (<-chan modelcall.StreamChunk, error) {
				return retryChunkStream(modelcall.StreamChunk{Content: "done", Usage: billed, AttemptUsage: &attempt, Done: true}), nil
			},
		}
		provider := &responseRetryProvider{inner: inner}
		var completion *modelcall.Completion
		var err error
		if stream {
			var chunks <-chan modelcall.StreamChunk
			chunks, err = provider.Stream(t.Context(), modelcall.CompletionRequest{})
			if err != nil {
				t.Fatalf("start stream: %v", err)
			}
			completion, _, err = modelcall.CollectStream(chunks)
		} else {
			completion, err = provider.Complete(t.Context(), modelcall.CompletionRequest{})
		}
		if err != nil || completion == nil || completion.ContextInputTokens() != 7 || completion.Usage.PromptTokens != 20 || completion.Usage.CacheReadInputTokens != 8 {
			t.Fatalf("stream=%v lost separated attempt and billing usage: %+v, %v", stream, completion, err)
		}
	}
}

func TestOpenAICompatClassifiesLengthTruncatedToolCallAsOutputTruncatedError(t *testing.T) {
	truncatedArgs := `{"path":"internal/foo.go","content":"package foo\nfunc bar() {`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "chatcmpl-trunc",
			"model": "gpt-4o",
			"choices": []map[string]any{{
				"index":         0,
				"finish_reason": "length",
				"message": map[string]any{
					"role":    "assistant",
					"content": "",
					"tool_calls": []map[string]any{{
						"id":   "call_write_1",
						"type": "function",
						"function": map[string]any{
							"name":      "write",
							"arguments": truncatedArgs,
						},
					}},
				},
			}},
			"usage": map[string]any{
				"prompt_tokens":     120,
				"completion_tokens": 4096,
				"total_tokens":      4216,
			},
		})
	}))
	defer srv.Close()

	provider := openaicompat.New("openai", srv.URL, "test-key", []modelinfo.Entry{{
		ID:                "gpt-4o",
		MaxTokens:         4096,
		ProviderMaxTokens: 16384,
	}})

	_, err := provider.Complete(context.Background(), modelcall.CompletionRequest{
		Model:     "gpt-4o",
		MaxTokens: 4096,
		Messages:  []api.Message{{Role: api.MessageRoleUser, Content: "write file"}},
	})
	if err == nil {
		t.Fatal("expected error for length-truncated tool call")
	}
	var emptyErr *failure.ProviderEmptyCompletionError
	if errors.As(err, &emptyErr) {
		t.Fatalf("expected ProviderOutputTruncatedError distinct from ProviderEmptyCompletionError, got %T: %v", err, err)
	}
	var truncErr *failure.ProviderOutputTruncatedError
	if !errors.As(err, &truncErr) {
		t.Fatalf("expected *failure.ProviderOutputTruncatedError, got %T: %v", err, err)
	}
	if !truncErr.ToolCallTruncated {
		t.Fatal("expected ToolCallTruncated = true")
	}
	if truncErr.ToolName != "write" {
		t.Fatalf("ToolName = %q, want %q", truncErr.ToolName, "write")
	}
	if truncErr.ToolArgsBytes != len(truncatedArgs) {
		t.Fatalf("ToolArgsBytes = %d, want %d", truncErr.ToolArgsBytes, len(truncatedArgs))
	}
	if truncErr.MaxTokens != 4096 {
		t.Fatalf("MaxTokens = %d, want 4096", truncErr.MaxTokens)
	}
	if truncErr.ConfiguredMaxTokens != 4096 || truncErr.ProviderMaxTokens != 16384 {
		t.Fatalf("ceilings = (%d, %d), want (4096, 16384)", truncErr.ConfiguredMaxTokens, truncErr.ProviderMaxTokens)
	}
	if !truncErr.LowOutputCeiling() {
		t.Fatal("expected LowOutputCeiling() = true when configured_max_tokens < provider_max_tokens")
	}
}

func TestLookupModelOutputCeilingDiagnosticSurfacesLowCeiling(t *testing.T) {
	reg := &Registry{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg.Register(openaicompat.New("anthropic", srv.URL, "key", []modelinfo.Entry{{
		ID:                "claude-sonnet-4-5",
		MaxTokens:         8192,
		ProviderMaxTokens: 64000,
	}}))

	ceiling := LookupModelOutputCeilingDiagnostic(context.Background(), reg, "anthropic", "claude-sonnet-4-5")
	if ceiling == nil {
		t.Fatal("expected output ceiling diagnostic to be found")
	}
	if ceiling.ConfiguredMaxTokens != 8192 || ceiling.ProviderMaxTokens != 64000 {
		t.Fatalf("ceiling = %+v, want 8192 / 64000", ceiling)
	}
	if !strings.Contains(ceiling.Diagnostic, "8192") || !strings.Contains(ceiling.Diagnostic, "64000") {
		t.Fatalf("diagnostic = %q, want configured and provider ceilings mentioned", ceiling.Diagnostic)
	}
}
