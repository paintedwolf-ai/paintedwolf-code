package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestConversationProbePreservesApplicationDefaultsAndShapes(t *testing.T) {
	var requests []modelcall.CompletionRequest
	stream := func(_ context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
		requests = append(requests, req)
		ch := make(chan modelcall.StreamChunk, 1)
		ch <- modelcall.StreamChunk{Done: true, Usage: modelcall.TokenUsage{PromptTokens: 12, CompletionTokens: 3}}
		close(ch)
		return ch, nil
	}
	result, err := ProbeConversation(t.Context(), stream)
	testutil.FailErr(t, "probe conversation", err)
	if len(result) != 2 || len(requests) != 2 {
		t.Fatalf("probes = %+v", result)
	}
	for i, req := range requests {
		if req.Think != modelcall.ThinkUnset || req.MaxTokens != 0 || req.Composition != modelcall.CompositionConversation {
			t.Fatalf("probe %d overrode application controls: %+v", i, req)
		}
		if !result[i].Accepted || result[i].Usage.PromptTokens != 12 {
			t.Fatalf("receipt = %+v", result[i])
		}
	}
	first := requests[0].Messages
	if len(first) != 5 || len(first[2].ToolCalls) != 2 || first[3].ToolResult.ToolCallID != first[2].ToolCalls[0].ID || first[4].ToolResult.ToolCallID != first[2].ToolCalls[1].ID {
		t.Fatalf("imported parallel tool history = %+v", first)
	}
	second := requests[1].Messages
	if len(second) != 7 || second[5].Role != api.MessageRoleAssistant || second[6].Role != api.MessageRoleSystem {
		t.Fatalf("host continuation timeline = %+v", second)
	}
}

func TestConversationProbeRequiresTerminalAcceptance(t *testing.T) {
	failure := errors.New("fixture transport failure")
	for _, kind := range []string{"request", "stream", "unterminated", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stream := func(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
				if kind == "request" {
					return nil, failure
				}
				ch := make(chan modelcall.StreamChunk, 1)
				if kind == "stream" {
					ch <- modelcall.StreamChunk{Err: failure}
				}
				if kind == "canceled" {
					cancel()
				} else {
					close(ch)
				}
				return ch, nil
			}
			result, err := ProbeConversation(ctx, stream)
			if err == nil || len(result) != 1 || result[0].Accepted {
				t.Fatalf("result = %+v, error = %v", result, err)
			}
		})
	}
}

func TestConversationProbeDoesNotExcludeBadModelAnswers(t *testing.T) {
	for _, failure := range []error{&failure.ProviderEmptyCompletionError{}, &failure.ProviderOutputTruncatedError{}, &failure.ProviderToolCallsInProseError{}} {
		stream := func(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
			ch := make(chan modelcall.StreamChunk, 1)
			ch <- modelcall.StreamChunk{Done: true, Err: failure}
			close(ch)
			return ch, nil
		}
		result, err := ProbeConversation(t.Context(), stream)
		testutil.FailErr(t, "accept transported model answer", err)
		if len(result) != 2 || !result[0].Accepted || result[0].ResponseCode == "" {
			t.Fatalf("result = %+v", result)
		}
	}
}

func TestUtilityProbeDoesNotRequireToolCalling(t *testing.T) {
	complete := func(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
		if req.Composition != modelcall.CompositionHostUtility || len(req.Tools) != 0 || req.Think != modelcall.ThinkOff {
			t.Fatalf("utility probe acquired coordinator controls: %+v", req)
		}
		return &modelcall.Completion{Content: "Worker completed."}, nil
	}
	results, err := ProbeUtility(t.Context(), complete)
	testutil.FailErr(t, "probe text utility", err)
	if len(results) != 1 || results[0].Stage != "utility_text" || !results[0].Accepted {
		t.Fatalf("utility stages = %+v", results)
	}
}
