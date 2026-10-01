package feedback

import "testing"

func TestCoordinatorPhaseExitRequired(t *testing.T) {
	base := WorkflowEvaluationContext{
		RunActive:          true,
		AdvanceWhenGateMet: "coordinator",
		CurrentGatesKnown:  true,
		CurrentGatesPassed: true,
		PhaseExitKind:      "proof",
	}
	if !base.CoordinatorPhaseExitRequired() {
		t.Fatal("satisfied coordinator proof phase must require its leave")
	}
	for name, mutate := range map[string]func(*WorkflowEvaluationContext){
		"gate open":      func(wf *WorkflowEvaluationContext) { wf.CurrentGatesPassed = false },
		"host advance":   func(wf *WorkflowEvaluationContext) { wf.AdvanceWhenGateMet = "auto" },
		"human approval": func(wf *WorkflowEvaluationContext) { wf.PhaseExitKind = "human_approval" },
		"terminal":       func(wf *WorkflowEvaluationContext) { wf.PhaseExitKind = "terminal" },
		"inactive":       func(wf *WorkflowEvaluationContext) { wf.RunActive = false },
		"unknown gates":  func(wf *WorkflowEvaluationContext) { wf.CurrentGatesKnown = false },
	} {
		t.Run(name, func(t *testing.T) {
			got := base
			mutate(&got)
			if got.CoordinatorPhaseExitRequired() {
				t.Fatal("phase must not request coordinator leave")
			}
		})
	}
}
