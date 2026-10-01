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
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	anthropicprovider "github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	vertexexpressprovider "github.com/lycaon/lycaon/internal/llm/providers/vertexexpress"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReasoningRecoveryKeepsRunAlive(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))
	t.Cleanup(modelcall.ResetTurnBudgetForTest)
	for _, transport := range []string{"complete", "sse", "json_stream"} {
		t.Run(transport, func(t *testing.T) {
			var requests []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode fixture request: %v", err)
				}
				requests = append(requests, body)
				content, reasoning, reason, tokens := "", "failed attempt private reasoning", "length", 24576
				if len(requests) > 1 {
					content, reasoning, reason, tokens = "Completed the next step.", "fresh reasoning", "stop", 20
				}
				if transport == "sse" {
					w.Header().Set("Content-Type", "text/event-stream")
					frame := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": content, "reasoning_content": reasoning}, "finish_reason": reason}}, "usage": map[string]int{"prompt_tokens": 100, "completion_tokens": tokens}}
					raw, _ := json.Marshal(frame)
					_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", raw)
				} else {
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content, "reasoning_content": reasoning}, "finish_reason": reason}}, "usage": map[string]int{"prompt_tokens": 100, "completion_tokens": tokens}})
				}
			}))
			defer server.Close()
			model := "@cf/zai-org/glm-5.3-flash"
			inner := openaicompat.New("fixture", server.URL, "fixture", []modelinfo.Entry{{ID: model, MaxTokens: 131072}})
			provider := &responseRetryProvider{inner: inner}
			req := modelcall.CompletionRequest{Model: model, Think: modelcall.ThinkHigh, Tools: []tools.ToolMeta{{Name: "read_file"}}, Debug: modelcall.RequestDebug{SessionID: transport}, Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Continue the work"}}}
			var got *modelcall.Completion
			var err error
			if transport == "complete" {
				got, err = provider.Complete(t.Context(), req)
			} else {
				ch, startErr := provider.Stream(t.Context(), req)
				if startErr != nil {
					t.Fatalf("start recovery stream: %v", startErr)
				}
				got, _, err = modelcall.CollectStreamWithProgress(ch, func(*modelcall.Completion) {})
			}
			if err != nil || got == nil || got.Content != "Completed the next step." {
				t.Fatalf("recovery result=%+v err=%v", got, err)
			}
			if got.Reasoning != "fresh reasoning" {
				t.Fatalf("failed reasoning leaked into recovered completion: %q", got.Reasoning)
			}
			if got.Usage.PromptTokens != 200 || got.Usage.CompletionTokens != 24596 {
				t.Fatalf("lost billed attempts: %+v", got.Usage)
			}
			if len(requests) != 2 || requests[0]["max_tokens"] != float64(32768) || requests[1]["max_tokens"] != float64(4096) || requests[0]["reasoning_effort"] != "high" || requests[1]["reasoning_effort"] != "low" {
				t.Fatalf("unexpected recovery controls: %+v", requests)
			}
			if modelcall.SessionStrictBudget(transport) {
				t.Fatal("successful recovery left the run permanently downgraded")
			}
		})
	}
}

func TestRecoveryRequiresChangedReasoningAndRespectsFixedOverrides(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))
	p := openaicompat.New("fixture", "http://unused", "fixture", nil)
	for _, tc := range []struct {
		name    string
		req     modelcall.CompletionRequest
		recover bool
	}{
		{"high", modelcall.CompletionRequest{Model: "glm-5.3-flash", Think: modelcall.ThinkHigh}, true},
		{"already low", modelcall.CompletionRequest{Model: "glm-5.3-flash", Think: modelcall.ThinkLow}, false},
		{"fixed", modelcall.CompletionRequest{Model: "glm-5.3-flash", ThinkingOverride: &modelcall.ThinkingOverride{Mode: "fixed", Effort: "high"}}, false},
		{"no reasoning", modelcall.CompletionRequest{Model: "unknown", Think: modelcall.ThinkOff, ThinkingOverrideStyle: modelinfo.ThinkStyleNone}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			req.AttemptBudget = &modelcall.CompletionBudget{}
			_, prepareErr := p.Prepare(req, false)
			testutil.FailErr(t, "prepare recovery controls", prepareErr)
			wrapper := &responseRetryProvider{inner: p}
			retry, err := wrapper.prepareResponseRetry(t.Context(), &req, 0, &failure.ProviderEmptyCompletionError{Terminal: true, Reason: "length"}, false)
			if err != nil || retry != tc.recover {
				t.Fatalf("retry=%v err=%v budget=%+v", retry, err, req.AttemptBudget)
			}
		})
	}
}

func TestCompletionTokenLimits(t *testing.T) {
	for _, tc := range []struct {
		name              string
		catalog, explicit int
		level             modelcall.ThinkLevel
		want              int
	}{
		{"medium", 131072, 0, modelcall.ThinkMedium, 24576},
		{"high", 131072, 0, modelcall.ThinkHigh, 32768},
		{"model ceiling", 20000, 0, modelcall.ThinkHigh, 20000},
		{"explicit small", 131072, 64, modelcall.ThinkHigh, 64},
		{"explicit above model", 8192, 16384, modelcall.ThinkHigh, 8192},
	} {
		req := modelcall.CompletionRequest{MaxTokens: tc.explicit, Tools: []tools.ToolMeta{{Name: "read"}}}
		if got := modelcall.CompletionTokenLimit(req, tc.catalog, 0, tc.level, true); got != tc.want {
			t.Errorf("%s: got=%d want=%d", tc.name, got, tc.want)
		}
	}
}

func TestReasoningCaptureRedactsBeforeTakingTail(t *testing.T) {
	var capture reasoningCapture
	capture.append(strings.Repeat("x", 3000) + "\nAuthorization: Bearer fixture-credential-value")
	summary := capture.summarize(nil, &failure.ProviderEmptyCompletionError{})
	if summary == nil || summary.ReasoningBytes == 0 || strings.Contains(summary.ReasoningTail, "fixture-credential-value") || len([]rune(summary.ReasoningTail)) > 2048 {
		t.Fatalf("unsafe capture: %+v", summary)
	}
	capture.append(strings.Repeat("x", maxReasoningCaptureBytes))
	summary = capture.summarize(nil, &failure.ProviderEmptyCompletionError{})
	if !summary.ReasoningCaptureOmitted || summary.ReasoningTail != "" || capture.text.Len() != 0 {
		t.Fatalf("oversize reasoning retained: %+v", summary)
	}
}

func TestNativeReasoningRecoveryControls(t *testing.T) {
	t.Cleanup(modelcall.ResetTurnBudgetForTest)
	req := modelcall.CompletionRequest{Model: "fixture", Think: modelcall.ThinkMedium, Tools: []tools.ToolMeta{{Name: "read"}}}
	for _, provider := range []modelcall.Provider{
		anthropicprovider.New("anthropic", "http://unused", "fixture", []modelinfo.Entry{{ID: "fixture", MaxTokens: 65536, ThinkStyle: "budget_tokens"}}),
		vertexexpressprovider.New("vertex", "http://unused", "fixture", []modelinfo.Entry{{ID: "fixture", MaxTokens: 65536, ThinkStyle: "budget_tokens"}}),
	} {
		t.Run(provider.ID(), func(t *testing.T) {
			req := req
			req.AttemptBudget = &modelcall.CompletionBudget{}
			if p, ok := provider.(*anthropicprovider.Provider); ok {
				p.Prepare(req, false)
			}
			if p, ok := provider.(*vertexexpressprovider.Provider); ok {
				p.Prepare(req)
			}
			if req.AttemptBudget.MaxTokens != 24576 || !req.AttemptBudget.CanReduceReasoning {
				t.Fatalf("initial native budget: %+v", req.AttemptBudget)
			}
			req.StrictBudget = true
			if p, ok := provider.(*anthropicprovider.Provider); ok {
				out := p.Prepare(req, false)
				if out.MaxTokens != 4096 || out.Thinking != nil {
					t.Fatalf("strict Anthropic controls: %+v", out)
				}
			}
			if p, ok := provider.(*vertexexpressprovider.Provider); ok {
				cfg := p.Prepare(req).GenerationConfig
				if cfg.MaxOutputTokens != 4096 || cfg.ThinkingConfig == nil || cfg.ThinkingConfig.ThinkingBudget == nil || *cfg.ThinkingConfig.ThinkingBudget != 1024 {
					t.Fatalf("strict Vertex controls: %+v", cfg)
				}
			}
		})
	}
}

func TestUtilityTruncationRejectedAcrossHTTPAdapters(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic", "vertex"} {
		t.Run(protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch protocol {
				case "openai":
					_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"Here's a thinking process"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":64}}`)
				case "anthropic":
					_, _ = fmt.Fprint(w, `{"content":[{"type":"text","text":"Here's a thinking process"}],"stop_reason":"max_tokens","usage":{"input_tokens":10,"output_tokens":64}}`)
				case "vertex":
					_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"Here's a thinking process"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":64}}`)
				}
			}))
			defer server.Close()
			var p modelcall.Provider
			switch protocol {
			case "openai":
				p = openaicompat.New("fixture", server.URL, "fixture", nil)
			case "anthropic":
				p = anthropicprovider.New("fixture", server.URL, "fixture", nil)
			case "vertex":
				p = vertexexpressprovider.New("fixture", server.URL, "fixture", nil)
			}
			req := modelcall.CompletionRequest{Model: "fixture", MaxTokens: 64, Composition: modelcall.CompositionHostUtility}
			completion, err := p.Complete(t.Context(), req)
			if _, ok := failure.AsProviderOutputTruncated(err); !ok || completion == nil || completion.Usage.CompletionTokens != 64 {
				t.Fatalf("utility accepted partial output or lost charges: completion=%+v err=%v", completion, err)
			}
			req.Composition = modelcall.CompositionConversation
			if _, err = p.Complete(t.Context(), req); err != nil {
				t.Fatalf("conversation continuation regressed: %v", err)
			}
		})
	}
}

func TestOutputLimitRecoveryNeverReplaysPayload(t *testing.T) {
	for _, first := range []modelcall.StreamChunk{{Content: "partial"}, {Content: " "}, {ToolCalls: []api.ToolCall{{Name: "write_file"}}, Progress: true}} {
		inner := &responseRetryStub{
			observeRequest: func(req modelcall.CompletionRequest) { req.RecordBudget(24576, "high", "low") },
			stream: func(int) (<-chan modelcall.StreamChunk, error) {
				return retryChunkStream(first, modelcall.StreamChunk{Err: &failure.ProviderEmptyCompletionError{Terminal: true, Reason: "length"}, Done: true}), nil
			},
		}
		p := &responseRetryProvider{inner: inner}
		ch, err := p.Stream(t.Context(), modelcall.CompletionRequest{})
		if err != nil {
			t.Fatalf("start partial-output fixture: %v", err)
		}
		_, _, err = modelcall.CollectStream(ch)
		if !errors.Is(err, failure.ErrProviderEmptyCompletion) || inner.streamCalls != 1 {
			t.Fatalf("replayed partial output: calls=%d err=%v", inner.streamCalls, err)
		}
	}
}

func TestReasoningRecoveryHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	inner := &responseRetryStub{
		observeRequest: func(req modelcall.CompletionRequest) { req.RecordBudget(24576, "high", "low") },
		complete: func(int) (*modelcall.Completion, error) {
			return &modelcall.Completion{Usage: modelcall.TokenUsage{PromptTokens: 50, CompletionTokens: 24576}}, &failure.ProviderEmptyCompletionError{Terminal: true, Reason: "length"}
		},
	}
	ctx = providerretry.WithRetryObserver(ctx, func(providerretry.RetryAttempt) { cancel() })
	p := &responseRetryProvider{inner: inner}
	out, err := p.Complete(ctx, modelcall.CompletionRequest{})
	if !errors.Is(err, context.Canceled) || inner.completeCalls != 1 || out.Usage.CompletionTokens != 24576 {
		t.Fatalf("canceled recovery: calls=%d result=%+v err=%v", inner.completeCalls, out, err)
	}
}
