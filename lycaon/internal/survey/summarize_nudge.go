package survey

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// StackedSurveyTools counts completed read, grep, and find results since sinceIdx.
func StackedSurveyTools(history []api.Message, sinceIdx int) int {
	var pending []string
	count := 0
	for i := sinceIdx; i < len(history); i++ {
		msg := history[i]
		switch msg.Role {
		case api.MessageRoleAssistant:
			for _, tc := range msg.ToolCalls {
				pending = append(pending, strings.TrimSpace(tc.Name))
			}
		case api.MessageRoleTool:
			if len(pending) == 0 {
				continue
			}
			toolName := pending[0]
			pending = pending[1:]
			if !isStackedSurveyTool(toolName) {
				continue
			}
			if msg.ToolResult == nil || msg.ToolResult.Outcome != api.ToolResultOutcomeCompleted {
				continue
			}
			count++
		case api.MessageRoleUser, api.MessageRoleSystem:
		}
	}
	return count
}

// SummarizeSinceBoundary reports whether a completed summarize call occurred since sinceIdx.
func SummarizeSinceBoundary(history []api.Message, sinceIdx int) bool {
	var pending []string
	for i := sinceIdx; i < len(history); i++ {
		msg := history[i]
		switch msg.Role {
		case api.MessageRoleAssistant:
			for _, tc := range msg.ToolCalls {
				pending = append(pending, strings.TrimSpace(tc.Name))
			}
		case api.MessageRoleTool:
			if len(pending) == 0 {
				continue
			}
			toolName := pending[0]
			pending = pending[1:]
			if toolName != "summarize" {
				continue
			}
			if msg.ToolResult == nil || msg.ToolResult.Outcome != api.ToolResultOutcomeCompleted {
				continue
			}
			return true
		case api.MessageRoleUser, api.MessageRoleSystem:
		}
	}
	return false
}

// TaskSinceBoundary reports whether a completed task() dispatch occurred since sinceIdx.
func TaskSinceBoundary(history []api.Message, sinceIdx int) bool {
	var pending []string
	for i := sinceIdx; i < len(history); i++ {
		msg := history[i]
		switch msg.Role {
		case api.MessageRoleAssistant:
			for _, tc := range msg.ToolCalls {
				pending = append(pending, strings.TrimSpace(tc.Name))
			}
		case api.MessageRoleTool:
			if len(pending) == 0 {
				continue
			}
			toolName := pending[0]
			pending = pending[1:]
			if toolName != "task" {
				continue
			}
			if msg.ToolResult == nil || msg.ToolResult.Outcome != api.ToolResultOutcomeCompleted {
				continue
			}
			return true
		case api.MessageRoleUser, api.MessageRoleSystem:
		}
	}
	return false
}

func isStackedSurveyTool(tool string) bool {
	switch strings.TrimSpace(tool) {
	case "read", "grep", "find":
		return true
	default:
		return false
	}
}
