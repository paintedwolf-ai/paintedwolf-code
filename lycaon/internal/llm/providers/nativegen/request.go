package nativegen

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/pkg/api"
)

type renderRequest struct {
	Messages []renderMessage `json:"messages"`
	Tools    []renderTool    `json:"tools"`
}

type renderMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content"`
	ToolCalls  []renderCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	Name       string       `json:"name,omitempty"`
}

type renderFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type renderTool struct {
	Type     string         `json:"type"`
	Function renderFunction `json:"function"`
}

type renderCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"function"`
}

func prepareRender(req modelcall.CompletionRequest) (renderRequest, error) {
	if req.ToolProtocol != modelcall.ToolProtocolNative {
		return renderRequest{}, errors.New("native generation: unsupported tool protocol")
	}
	result := renderRequest{Tools: make([]renderTool, 0, len(req.Tools))}
	for _, tool := range req.Tools {
		if !req.ToolsCallable() {
			break
		}
		if tool.Name == "" {
			return renderRequest{}, errors.New("native generation: unnamed tool")
		}
		parameters := tool.ArgsSchema
		if parameters == nil {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		result.Tools = append(result.Tools, renderTool{Type: "function", Function: renderFunction{Name: tool.Name, Description: tool.Description, Parameters: parameters}})
	}
	for _, message := range providerwire.SystemPreamble(req.Messages) {
		if api.IsAgentNoteMessage(message) {
			continue
		}
		wire := renderMessage{Role: string(message.Role), Content: message.Content}
		switch message.Role {
		case api.MessageRoleSystem, api.MessageRoleUser:
			if len(message.ToolCalls) > 0 || message.ToolResult != nil {
				return renderRequest{}, errors.New("native generation: tool payload on a non-tool turn")
			}
		case api.MessageRoleAssistant:
			for _, call := range message.ToolCalls {
				if call.ID == "" || call.Name == "" || call.ArgsRef != nil || call.ArgsMalformed || call.ArgsTruncated {
					return renderRequest{}, errors.New("native generation: incomplete tool-call history")
				}
				item := renderCall{ID: call.ID, Type: "function"}
				item.Function.Name = call.Name
				item.Function.Arguments = providerwire.ToolArgs(call.Args)
				wire.ToolCalls = append(wire.ToolCalls, item)
			}
		case api.MessageRoleTool:
			if message.ToolResult == nil || message.ToolResult.Tool == "" {
				return renderRequest{}, errors.New("native generation: tool result has no tool identity")
			}
			wire.ToolCallID = message.ToolResult.ToolCallID
			wire.Name = message.ToolResult.Tool
			if wire.Content == "" {
				wire.Content = message.ToolResult.Content
			}
		default:
			return renderRequest{}, fmt.Errorf("native generation: unsupported message role %q", message.Role)
		}
		result.Messages = append(result.Messages, wire)
	}
	if len(result.Messages) == 0 {
		return renderRequest{}, errors.New("native generation: empty conversation")
	}
	return result, nil
}

func outputSchema(format *modelcall.ResponseFormat) (json.RawMessage, error) {
	if format == nil {
		return nil, nil
	}
	switch format.Type {
	case modelcall.ResponseFormatJSONObject:
		return json.RawMessage(`{"type":"object"}`), nil
	case modelcall.ResponseFormatJSONSchema:
		if len(format.Schema) == 0 || len(format.Schema) > 256<<10 || !json.Valid(format.Schema) {
			return nil, errors.New("native generation: invalid output schema")
		}
		return append(json.RawMessage(nil), format.Schema...), nil
	default:
		return nil, errors.New("native generation: unsupported response format")
	}
}
