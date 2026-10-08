package promptloop

import (
	"maps"
	"slices"

	"github.com/lycaon/lycaon/pkg/api"
)

// bindReviewResult retains the offered workflow phase even when execution
// advances the run before the tool result reaches transcript storage.
func bindReviewResult(msg *api.Message, st *promptLoopTurnState) {
	if msg.ToolResult == nil || msg.ToolResult.Tool != "submit_verdict" {
		return
	}
	binding := completionReportBinding(st)
	if binding.RunID == "" {
		return
	}
	msg.WorkflowRunID = binding.RunID
	result := *msg.ToolResult
	result.Feedback = slices.Clone(result.Feedback)
	for i := range result.Feedback {
		details := maps.Clone(result.Feedback[i].Details)
		if details == nil {
			details = map[string]any{}
		}
		details["workflow_phase"] = binding.Phase
		result.Feedback[i].Details = details
	}
	msg.ToolResult = &result
}
