package toolhost

import (
	"encoding/json"
	"slices"

	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/pkg/api"
)

// Delayed explanations use the transcript through the held action.
func rationaleMessagesThroughAction(messages []api.Message, toolCallID string) []api.Message {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != api.MessageRoleAssistant {
			continue
		}
		for _, call := range messages[i].ToolCalls {
			if call.ID == toolCallID {
				return messages[:i+1]
			}
		}
	}
	return nil
}

// Only observations preceding the held action inform its explanation.
func recentRationaleResults(messages []api.Message, start int, toolCallID string) string {
	end := -1
	for i, message := range messages {
		if message.Role == api.MessageRoleAssistant {
			for _, call := range message.ToolCalls {
				if call.ID == toolCallID {
					end = i
				}
			}
		}
	}
	if end < 0 || start < 0 || start > end {
		return ""
	}
	type receipt struct {
		Tool    string                `json:"tool"`
		Outcome api.ToolResultOutcome `json:"outcome"`
		Content string                `json:"content"`
	}
	var recent []receipt
	for i := end - 1; i >= start && len(recent) < 3; i-- {
		message := messages[i]
		if message.Role != api.MessageRoleTool || message.ToolResult == nil {
			continue
		}
		result := message.ToolResult
		recent = append(recent, receipt{Tool: result.Tool, Outcome: result.Outcome, Content: runeclamp.Clamp(result.Content, 450)})
	}
	if len(recent) == 0 {
		return ""
	}
	slices.Reverse(recent)
	data, err := json.Marshal(recent)
	if err != nil {
		return ""
	}
	return string(data)
}
