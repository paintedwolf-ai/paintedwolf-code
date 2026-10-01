package progress

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// TurnHasProgressGatedTool reports whether turnTools includes a progress-gated tool call.
func TurnHasProgressGatedTool(turnTools []string) bool {
	for _, tool := range turnTools {
		if IsProgressGatedTool(tool) {
			return true
		}
	}
	return false
}

// FirstProgressGatedTool returns the first progress-gated tool name in turnTools, or "task" when none
// is present (nudge template default).
func FirstProgressGatedTool(turnTools []string) string {
	for _, tool := range turnTools {
		if IsProgressGatedTool(tool) {
			return strings.TrimSpace(tool)
		}
	}
	return "task"
}

// ProgressGatedToolSinceBoundary reports whether a completed progress-gated tool call occurred since
// sinceIdx in the transcript.
func ProgressGatedToolSinceBoundary(history []api.Message, sinceIdx int) bool {
	return progressGatedToolSinceBoundary(history, sinceIdx, true)
}

// ProgressGatedToolAttemptedSinceBoundary reports whether a progress-gated tool was attempted since
// sinceIdx, including guard rejects from PROGRESS_MISSING.
func ProgressGatedToolAttemptedSinceBoundary(history []api.Message, sinceIdx int) bool {
	return progressGatedToolSinceBoundary(history, sinceIdx, false)
}

func progressGatedToolSinceBoundary(history []api.Message, sinceIdx int, completedOnly bool) bool {
	for i := sinceIdx; i < len(history); i++ {
		msg := history[i]
		if msg.Role != api.MessageRoleTool || msg.ToolResult == nil {
			continue
		}
		if !IsProgressGatedTool(strings.TrimSpace(msg.ToolResult.Tool)) {
			continue
		}
		if completedOnly && msg.ToolResult.Outcome != api.ToolResultOutcomeCompleted {
			continue
		}
		return true
	}
	return false
}
