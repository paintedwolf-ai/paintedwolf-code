package llm

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// ConversationProbe records transport acceptance, never task correctness.
type ConversationProbe struct {
	Stage        string               `json:"stage"`
	Accepted     bool                 `json:"accepted"`
	Usage        modelcall.TokenUsage `json:"usage"`
	ResponseCode string               `json:"response_code,omitempty"`
}

// ProbeUtility checks the text-only transport used by a utility model.
func ProbeUtility(ctx context.Context, complete func(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error)) ([]ConversationProbe, error) {
	req := modelcall.CompletionRequest{
		Composition: modelcall.CompositionHostUtility,
		Think:       modelcall.ThinkOff,
		Messages: []api.Message{
			{Role: api.MessageRoleSystem, Content: "Summarize project updates briefly."},
			{Role: api.MessageRoleUser, Content: "The worker finished and verification passed."},
		},
		Debug: modelcall.RequestDebug{Surface: "provider-compatibility", Purpose: "utility_text"},
	}
	result := ConversationProbe{Stage: "utility_text"}
	response, err := complete(ctx, req)
	if err != nil {
		result, err = probeResponseError(result, err)
	} else if response == nil {
		err = fmt.Errorf("provider returned no utility receipt")
	} else {
		result.Accepted, result.Usage = true, response.Usage
	}
	return []ConversationProbe{result}, err
}

// ProbeConversation exercises imported tool history and a subsequent host turn
// with the same request defaults and replay projection as coordinator traffic.
func ProbeConversation(ctx context.Context, stream func(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error)) ([]ConversationProbe, error) {
	req := modelcall.CompletionRequest{
		Messages: []api.Message{
			{Role: api.MessageRoleSystem, Content: "Help with the project. Keep the response brief."},
			{Role: api.MessageRoleUser, Content: "Read the two status files and summarize them."},
			{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
				{ID: "call_status_a", Name: "read", Args: map[string]any{"path": "a.txt"}},
				{ID: "call_status_b", Name: "read", Args: map[string]any{"path": "b.txt"}},
			}},
			{Role: api.MessageRoleTool, Content: "Ready.", ToolResult: &api.ToolResult{ToolCallID: "call_status_a"}},
			{Role: api.MessageRoleTool, Content: "Ready.", ToolResult: &api.ToolResult{ToolCallID: "call_status_b"}},
		},
		Tools: []tools.ToolMeta{{Name: "read", Description: "Read a project status file.", ArgsSchema: map[string]any{
			"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"},
		}}},
	}
	results := make([]ConversationProbe, 0, 2)
	for _, stage := range []string{"imported_tool_results", "host_continuation"} {
		req.Debug = modelcall.RequestDebug{Surface: "provider-compatibility", Purpose: stage}
		receipt, err := probeRequest(ctx, stream, req, stage)
		results = append(results, receipt)
		if err != nil {
			return results, err
		}
		// The host continuation is a controlled transcript shape, not the model's answer.
		req.Messages = append(req.Messages, api.Message{Role: api.MessageRoleAssistant, Content: "Both files are ready."},
			api.Message{Role: api.MessageRoleSystem, Content: "The worker finished. Its verification passed. Continue with the handoff."})
	}
	return results, nil
}

func probeRequest(ctx context.Context, stream func(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error), req modelcall.CompletionRequest, stage string) (ConversationProbe, error) {
	result := ConversationProbe{Stage: stage}
	terminal := false
	chunks, err := stream(ctx, req)
	if err != nil {
		return probeResponseError(result, err)
	}
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case chunk, ok := <-chunks:
			if !ok {
				if !terminal {
					return result, fmt.Errorf("provider stream ended without a terminal receipt")
				}
				result.Accepted = true
				return result, nil
			}
			if chunk.Err != nil {
				result.Usage = chunk.Usage
				return probeResponseError(result, chunk.Err)
			}
			if chunk.Done {
				terminal = true
				result.Usage = chunk.Usage
			}
		}
	}
}

// A completed but unusable answer belongs in the benchmark, not in a transport gate.
func probeResponseError(result ConversationProbe, err error) (ConversationProbe, error) {
	switch {
	case errors.Is(err, failure.ErrProviderEmptyCompletion):
		result.ResponseCode = string(api.NoticeCodeProviderEmptyCompletion)
	case errors.Is(err, failure.ErrProviderOutputTruncated):
		result.ResponseCode = "provider_output_truncated"
	case errors.Is(err, failure.ErrProviderToolCallsInProse):
		result.ResponseCode = string(api.NoticeCodeProviderToolCallsInProse)
	default:
		return result, err
	}
	result.Accepted = true
	return result, nil
}
