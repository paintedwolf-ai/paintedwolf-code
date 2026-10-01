package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// GateCheckResult captures gate evaluation outcome for Advance and auto-advance.
type GateCheckResult struct {
	Reason       string
	FailedGate   string
	FailedLeaves []string
}

// GateEvaluator checks whether the current phase gate is satisfied before advance.
type GateEvaluator interface {
	PhaseGateMet(ctx context.Context, manifest workflowdef.Manifest, run *api.WorkflowRun, vars map[string]any) (bool, GateCheckResult, error)
}

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
			if !gateSatisfiedInVars(vars, gate) {
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
			if gateSatisfiedInVars(vars, gate) {
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

func gateSatisfiedInVars(vars map[string]any, gate string) bool {
	gate = strings.TrimSpace(gate)
	if gate == "" {
		return true
	}
	if vars == nil {
		return false
	}
	gates, _ := vars["gates"].(map[string]any)
	if gates == nil {
		return false
	}
	ok, _ := gates[gate].(bool)
	return ok
}

// PhaseGateUnmetError indicates advance was blocked by an unsatisfied phase gate.
type PhaseGateUnmetError struct {
	Phase        string
	Reason       string
	FailedGate   string
	FailedLeaves []string
	Replayed     bool
}

func (e *PhaseGateUnmetError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("phase gate unmet: %s", e.Reason)
	}
	return "phase gate unmet"
}

func IsPhaseGateUnmet(err error) (*PhaseGateUnmetError, bool) {
	var pe *PhaseGateUnmetError
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}

// SetGateSatisfied records one host-managed gate result.
func SetGateSatisfied(vars map[string]any, gate string, satisfied bool) map[string]any {
	vars = cloneVars(vars)
	gates, _ := vars["gates"].(map[string]any)
	if gates == nil {
		gates = map[string]any{}
		vars["gates"] = gates
	}
	gates[gate] = satisfied
	return vars
}
