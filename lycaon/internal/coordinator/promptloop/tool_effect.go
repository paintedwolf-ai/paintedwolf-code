package promptloop

import (
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

func toolWorkflowAdvanced(tool string, facts guidance.ToolResultFacts) bool {
	if facts.WorkflowAdvanced != nil {
		return *facts.WorkflowAdvanced
	}
	return tool != "update_progress"
}

// advancesRepetitionContext distinguishes material progress from bookkeeping.
func (run completedToolRun) advancesRepetitionContext() bool {
	if !run.contract.MutatesWorld() {
		return false
	}
	facts := guidance.ToolResultFacts{}
	if run.toolCtx.Effects.Out != nil {
		facts = run.toolCtx.Effects.Out.Facts
	}
	return toolWorkflowAdvanced(run.call.Name, facts)
}

type batchProgressOutcome struct{ Advanced, Blocked bool }

// observe consumes settled invocation facts before transcript projection.
func (out *batchProgressOutcome) observe(advanced bool, result *api.ToolResult) {
	if result == nil {
		return
	}
	switch result.Outcome {
	case api.ToolResultOutcomeRejected, api.ToolResultOutcomeError:
		out.Blocked = true
	default:
		out.Advanced = out.Advanced || advanced
	}
}
