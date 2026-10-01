package llm

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestToolCompatibilityRequiresActualReceiptReplay(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(map[bool]string{false: "correct receipt", true: "invented receipt"}[wrong], func(t *testing.T) {
			calls := 0
			stages, err := ToolCompatibility(t.Context(), func(_ context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
				calls++
				if req.Think != modelcall.ThinkUnset || req.MaxTokens != 0 {
					t.Fatal("probe changed application request defaults")
				}
				call := api.ToolCall{ID: "call-1", Name: "read_status", Args: map[string]any{}}
				if calls == 2 {
					var payload map[string]string
					testutil.FailErr(t, "read fixture receipt", json.Unmarshal([]byte(req.Messages[len(req.Messages)-2].Content), &payload))
					call = api.ToolCall{ID: "call-2", Name: "record_status", Args: map[string]any{"receipt": payload["receipt"]}}
					if wrong {
						call.Args["receipt"] = "invented"
					}
					if req.Messages[len(req.Messages)-1].Role != api.MessageRoleSystem {
						t.Fatal("host continuation missing")
					}
				}
				ch := make(chan modelcall.StreamChunk, 1)
				ch <- modelcall.StreamChunk{ToolCalls: []api.ToolCall{call}, Done: true}
				close(ch)
				return ch, nil
			})
			if wrong && err == nil {
				t.Fatal("invented receipt passed")
			}
			if !wrong {
				testutil.FailErr(t, "verify tool compatibility", err)
			}
			if len(stages.Exchanges) != 2 || stages.Exchanges[1].Response == nil {
				t.Fatal("diagnostic lost the response needed to investigate replay")
			}
			if calls != 2 || len(stages.Stages) != 2 {
				t.Fatalf("calls=%d stages=%+v", calls, stages)
			}
		})
	}
}

func TestToolCompatibilityDoesNotAcceptProseOrTransportFailure(t *testing.T) {
	for _, failure := range []bool{false, true} {
		stages, err := ToolCompatibility(t.Context(), func(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
			if failure {
				return nil, errors.New("transport unavailable")
			}
			ch := make(chan modelcall.StreamChunk, 1)
			ch <- modelcall.StreamChunk{Content: "I called the tool successfully.", Done: true}
			close(ch)
			return ch, nil
		})
		if err == nil {
			t.Fatalf("no tool call passed: %+v", stages)
		}
	}
}

func TestToolDiagnosticRequiresTerminalReceiptAndHonorsCancellation(t *testing.T) {
	partial := make(chan modelcall.StreamChunk, 1)
	partial <- modelcall.StreamChunk{ToolCalls: []api.ToolCall{{ID: "call", Name: "read_status", Args: map[string]any{}}}}
	close(partial)
	if _, err := collectToolDiagnostic(t.Context(), partial); err == nil {
		t.Fatal("partial stream passed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := collectToolDiagnostic(ctx, make(chan modelcall.StreamChunk)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestToolCompatibilityRejectsMalformedAndTruncatedCalls(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		_, err := ToolCompatibility(t.Context(), func(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
			chunks := make(chan modelcall.StreamChunk, 1)
			chunks <- modelcall.StreamChunk{Done: true, ToolCalls: []api.ToolCall{{ID: "call", Name: "read_status", Args: map[string]any{}, ArgsMalformed: !truncated, ArgsTruncated: truncated}}}
			close(chunks)
			return chunks, nil
		})
		var failure *ToolCompatibilityError
		if !errors.As(err, &failure) || failure.Code != "tool_call_arguments" {
			t.Fatalf("invalid wire arguments passed: %v", err)
		}
	}
}
