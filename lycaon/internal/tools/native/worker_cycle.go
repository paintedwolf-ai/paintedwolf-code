package native

import (
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerToolEndsCycle recognizes an accepted worker completion or decision pause.
func WorkerToolEndsCycle(toolName string, result *api.ToolResult) bool {
	if result == nil || result.Outcome != api.ToolResultOutcomeCompleted {
		return false
	}
	return toolName == workertools.CompleteLegTool || toolName == workertools.RequestDecisionTool
}
