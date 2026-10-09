package toolguard

import (
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func RejectFanoutTask(reason string, task *api.WorkerTask) error {
	return &tools.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"tool": "task", "field": "workflow_work_id", "reason": reason, "workflow_work_id": task.WorkflowWorkID, "workflow_phase": task.WorkflowPhase}}
}
