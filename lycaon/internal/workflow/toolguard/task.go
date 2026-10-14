package toolguard

import (
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

func RejectFanoutTask(reason string, task *api.WorkerTask) error {
	reject := &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"tool": "task", "field": "workflow_work_id", "reason": reason, "workflow_work_id": task.WorkflowWorkID, "workflow_phase": task.WorkflowPhase}}
	if reason == "not_a_planned_fanout_phase" {
		reject.Data["inapplicable_fields"] = []string{"workflow_work_id"}
	}
	return reject
}
