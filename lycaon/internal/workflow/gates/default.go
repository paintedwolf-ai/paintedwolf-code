package gates

import (
	"context"
	"strings"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// GateCheckResult captures gate evaluation outcome for Advance and auto-advance.

// GateEvaluator checks whether the current phase gate is satisfied before advance.

// FailClosedGateEvaluator blocks unresolved domain gates.
type FailClosedGateEvaluator struct{}

func (FailClosedGateEvaluator) PhaseGateMet(_ context.Context, manifest workflowdef.Manifest, run *api.WorkflowRun, vars map[string]any) (bool, GateCheckResult, error) {
	if run == nil {
		return true, GateCheckResult{}, nil
	}
	def, ok := manifest.PhaseForRun(run, run.CurrentPhase)
	if !ok {
		return true, GateCheckResult{}, nil
	}
	cw := strings.TrimSpace(def.CompleteWhen)
	switch cw {
	case workflowdef.CompleteWhenGatesSatisfied:
		var failed []string
		for _, gate := range def.Gates {
			if !SatisfiedInVars(vars, gate) {
				failed = append(failed, gate)
			}
		}
		if len(failed) > 0 {
			return false, GateCheckResult{
				Reason:       workflowdef.CompleteWhenGatesSatisfied,
				FailedGate:   failed[0],
				FailedLeaves: failed,
			}, nil
		}
		return true, GateCheckResult{}, nil
	case "":
		return true, GateCheckResult{}, nil
	default:
		if strings.HasPrefix(cw, workflowdef.CompleteWhenGateSatisfied) {
			gate := strings.TrimPrefix(cw, workflowdef.CompleteWhenGateSatisfied)
			if SatisfiedInVars(vars, gate) {
				return true, GateCheckResult{Reason: cw}, nil
			}
			return false, GateCheckResult{
				Reason:       cw,
				FailedGate:   gate,
				FailedLeaves: []string{gate},
			}, nil
		}
		if workflowdef.IsKnownCompleteWhen(cw) {
			return false, GateCheckResult{
				Reason:       cw,
				FailedGate:   cw,
				FailedLeaves: []string{cw},
			}, nil
		}
		return false, GateCheckResult{
			Reason:       "unknown complete_when: " + cw,
			FailedGate:   cw,
			FailedLeaves: []string{cw},
		}, nil
	}
}

// runstate.SetGateSatisfied records one host-managed gate result.
