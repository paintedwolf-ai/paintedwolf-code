package inject

import (
	"strings"

	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/pkg/api"
)

func WorkflowEvaluation(frame CoordinatorTurnFrame) feedback.WorkflowEvaluationContext {
	runCtx := frame.RunContext
	out := feedback.WorkflowEvaluationContext{
		WorkflowID:         strings.TrimSpace(runCtx.WorkflowID),
		CurrentPhase:       strings.TrimSpace(runCtx.CurrentPhase),
		FailedLeaves:       append([]string(nil), runCtx.FailedLeaves...),
		RunActive:          strings.TrimSpace(runCtx.RunStatus) == string(api.WorkflowRunStatusRunning),
		AdvanceWhenGateMet: strings.TrimSpace(runCtx.AdvanceWhenGateMet),
	}
	if frame.Runtime.PhaseExit != nil {
		out.PhaseExitKind = frame.Runtime.PhaseExit.Kind
	}
	for _, phase := range frame.Runtime.Phases {
		if phase.ID != out.CurrentPhase {
			continue
		}
		out.CurrentGatesKnown = false
		out.CurrentGatesPassed = false
		out.FailedLeaves = nil
		for _, gate := range phase.Gates {
			if gate.Dormant {
				continue
			}
			out.CurrentGatesKnown = true
			if gate.Satisfied {
				continue
			}
			out.FailedLeaves = append(out.FailedLeaves, gate.ID)
		}
		out.CurrentGatesPassed = out.CurrentGatesKnown && len(out.FailedLeaves) == 0
		break
	}
	return out
}
