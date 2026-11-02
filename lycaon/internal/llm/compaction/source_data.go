package compaction

import (
	"encoding/json"
	"fmt"

	"github.com/lycaon/lycaon/pkg/api"
)

type summaryToolCall struct {
	ID        string                    `json:"id"`
	Name      string                    `json:"name"`
	Args      map[string]any            `json:"arguments,omitempty"`
	ArgsRef   *api.ChatContentReference `json:"arguments_ref,omitempty"`
	Truncated bool                      `json:"arguments_truncated,omitempty"`
	Malformed bool                      `json:"arguments_malformed,omitempty"`
}

// sourceData preserves tool linkage as quoted evidence, without replay authority.
func sourceData(msg ContextMessage) (string, error) {
	content, _, _, _ := projectCompactionMessage(msg)
	if len(msg.ToolCalls) == 0 && msg.ToolCallID == "" && msg.ToolName == "" {
		return content, nil
	}
	data := struct {
		Content    string            `json:"content,omitempty"`
		Calls      []summaryToolCall `json:"tool_calls,omitempty"`
		ResultID   string            `json:"result_call_id,omitempty"`
		ResultTool string            `json:"result_tool,omitempty"`
	}{Content: content, ResultID: msg.ToolCallID, ResultTool: msg.ToolName}
	for _, call := range msg.ToolCalls {
		data.Calls = append(data.Calls, summaryToolCall{ID: call.ID, Name: call.Name, Args: call.Args,
			ArgsRef: call.ArgsRef, Truncated: call.ArgsTruncated, Malformed: call.ArgsMalformed})
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("encode compaction tool evidence: %w", err)
	}
	return string(encoded), nil
}
