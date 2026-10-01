package feedback

import "strings"

// WorkflowEvaluationContext is workflow gate template data.
type WorkflowEvaluationContext struct {
	WorkflowID         string
	CurrentPhase       string
	FailedLeaves       []string
	RunActive          bool
	AdvanceWhenGateMet string
	CurrentGatesKnown  bool
	CurrentGatesPassed bool
	PhaseExitKind      string
}

// CoordinatorPhaseExitRequired reports whether coordinator proof may advance the phase.
func (wf WorkflowEvaluationContext) CoordinatorPhaseExitRequired() bool {
	if !wf.RunActive || !wf.CurrentGatesKnown || !wf.CurrentGatesPassed {
		return false
	}
	if strings.TrimSpace(wf.AdvanceWhenGateMet) != "coordinator" {
		return false
	}
	switch strings.TrimSpace(wf.PhaseExitKind) {
	case "proof", "review_loop":
		return true
	default:
		return false
	}
}

// GateFeedbackContext builds gate expression data.
func GateFeedbackContext(wf WorkflowEvaluationContext, extras map[string]any) map[string]any {
	ctx := map[string]any{
		"workflow_id":           strings.TrimSpace(wf.WorkflowID),
		"current_phase":         strings.TrimSpace(wf.CurrentPhase),
		"failed_leaves":         append([]string(nil), wf.FailedLeaves...),
		"advance_when_gate_met": strings.TrimSpace(wf.AdvanceWhenGateMet),
		"current_gates_known":   wf.CurrentGatesKnown,
		"current_gates_passed":  wf.CurrentGatesPassed,
		"phase_exit_kind":       strings.TrimSpace(wf.PhaseExitKind),
	}
	for k, v := range extras {
		ctx[k] = v
	}
	return ctx
}
