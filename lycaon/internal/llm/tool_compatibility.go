package llm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type ToolCompatibilityError struct {
	Code   string
	Stage  string
	Detail string
}

func (e *ToolCompatibilityError) Error() string {
	return fmt.Sprintf("The %s check %s", e.Stage, e.Detail)
}

type ToolReplayOrigin struct {
	ProviderID string `json:"provider_id"`
	Model      string `json:"model"`
	SHA256     string `json:"sha256"`
}

type ToolExchange struct {
	ReasoningOrigin *ToolReplayOrigin `json:"reasoning_origin,omitempty"`
	Stage           string            `json:"stage"`
	Messages        []api.Message     `json:"messages"`
	Tools           []tools.ToolMeta  `json:"tools"`
	Response        *api.Message      `json:"response,omitempty"`
}

type ToolCompatibilityResult struct {
	Stages    []ConversationProbe `json:"stages"`
	Exchanges []ToolExchange      `json:"exchanges"`
}

// ToolCompatibility checks actual calls and result replay without grading prose.
func ToolCompatibility(ctx context.Context, stream func(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error)) (ToolCompatibilityResult, error) {
	var observation ToolCompatibilityResult
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return observation, err
	}
	receipt := hex.EncodeToString(nonce[:])
	req := modelcall.CompletionRequest{
		Messages: []api.Message{
			{Role: api.MessageRoleSystem, Content: "Use the available tools to read the status, then record its receipt."},
			{Role: api.MessageRoleUser, Content: "Read the project status and record its receipt."},
		},
		Tools: []tools.ToolMeta{compatibilityTool("read_status", "Read the project status and its receipt.", nil)},
	}
	for _, stage := range []string{"tool_call", "tool_result_replay"} {
		req.Debug = modelcall.RequestDebug{Surface: "provider-compatibility", Purpose: stage}
		observation.Exchanges = append(observation.Exchanges, ToolExchange{Stage: stage, Messages: append([]api.Message(nil), req.Messages...), Tools: req.Tools})
		chunks, err := stream(ctx, req)
		if err != nil {
			return observation, err
		}
		response, err := collectToolDiagnostic(ctx, chunks)
		observation.Exchanges[len(observation.Exchanges)-1].Response = &api.Message{Role: api.MessageRoleAssistant, Content: response.Content, ToolCalls: response.ToolCalls, ModelReasoning: response.ModelReasoning()}
		if reasoning := response.ModelReasoning(); reasoning != nil {
			raw, marshalErr := json.Marshal(reasoning)
			if marshalErr != nil {
				return observation, marshalErr
			}
			observation.Exchanges[len(observation.Exchanges)-1].ReasoningOrigin = &ToolReplayOrigin{ProviderID: reasoning.ProviderID, Model: reasoning.Model, SHA256: fmt.Sprintf("%x", sha256.Sum256(raw))}
		}
		if err != nil {
			observation.Stages = append(observation.Stages, ConversationProbe{Stage: stage, Usage: response.Usage})
			return observation, err
		}
		result := ConversationProbe{Stage: stage, Usage: response.Usage}
		if len(response.ToolCalls) != 1 {
			observation.Stages = append(observation.Stages, result)
			return observation, &ToolCompatibilityError{Code: "tool_call_count", Stage: stage, Detail: "did not return one structured tool call."}
		}
		call := response.ToolCalls[0]
		if call.ID == "" || call.Name != req.Tools[0].Name {
			observation.Stages = append(observation.Stages, result)
			return observation, &ToolCompatibilityError{Code: "tool_call_identity", Stage: stage, Detail: "returned an unexpected tool or missing call identity."}
		}
		if call.ArgsMalformed || call.ArgsTruncated || stage == "tool_call" && len(call.Args) != 0 || stage == "tool_result_replay" && (len(call.Args) != 1 || call.Args["receipt"] != receipt) {
			observation.Stages = append(observation.Stages, result)
			return observation, &ToolCompatibilityError{Code: "tool_call_arguments", Stage: stage, Detail: "returned arguments that do not match the requested receipt."}
		}
		result.Accepted = true
		observation.Stages = append(observation.Stages, result)
		req.Messages = append(req.Messages, api.Message{Role: api.MessageRoleAssistant, Content: response.Content, ToolCalls: response.ToolCalls, ModelReasoning: response.ModelReasoning()})
		payload, err := json.Marshal(map[string]string{"status": "ready", "receipt": receipt})
		if err != nil {
			return observation, err
		}
		req.Messages = append(req.Messages, api.Message{Role: api.MessageRoleTool, Content: string(payload), ToolResult: &api.ToolResult{ToolCallID: call.ID}}, api.Message{Role: api.MessageRoleSystem, Content: "The status is available. Record its receipt."})
		req.Tools = []tools.ToolMeta{compatibilityTool("record_status", "Record the receipt returned by the status tool.", new("receipt"))}
	}
	return observation, nil
}

func compatibilityTool(name, description string, argument *string) tools.ToolMeta {
	schema := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	if argument != nil {
		schema["properties"] = map[string]any{*argument: map[string]any{"type": "string"}}
		schema["required"] = []string{*argument}
	}
	return tools.ToolMeta{Name: name, Description: description, ArgsSchema: schema}
}

// A partial stream cannot establish compatibility, even if it contains a call.
func collectToolDiagnostic(ctx context.Context, chunks <-chan modelcall.StreamChunk) (*modelcall.Completion, error) {
	result := &modelcall.Completion{}
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case chunk, ok := <-chunks:
			if !ok {
				return result, fmt.Errorf("the compatibility stream ended without a terminal receipt")
			}
			if chunk.ResetReasoning {
				result.Reasoning = ""
				result.ReasoningDetails = nil
			}
			result.Content += chunk.Content
			result.Reasoning += chunk.Reasoning
			if len(chunk.ToolCalls) > 0 {
				result.ToolCalls = chunk.ToolCalls
			}
			if len(chunk.ReasoningDetails) > 0 {
				result.ReasoningDetails = chunk.ReasoningDetails
			}
			if chunk.ProviderID != "" {
				result.ProviderID = chunk.ProviderID
				result.Model = chunk.Model
			}
			if chunk.Usage.Reported() {
				result.Usage = chunk.Usage
			}
			if chunk.Err != nil {
				return result, chunk.Err
			}
			if chunk.Done {
				return result, nil
			}
		}
	}
}
