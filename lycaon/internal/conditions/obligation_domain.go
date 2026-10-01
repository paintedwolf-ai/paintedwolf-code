package conditions

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// ObligationGatePrefix identifies obligation gate leaves.
const ObligationGatePrefix = "obligation_settled:"

// ObligationStatusReader reports run-scoped status for the obligation a named
// phase declares, so a phase the run has since left still reads its own terms.
type ObligationStatusReader interface {
	Status(ctx context.Context, workflowRunID, phase string) (api.WorkflowRunObligation, error)
}

// RegisterObligationDomain adds ledger-backed obligation gates.
func RegisterObligationDomain(reg *ConditionRegistry, resolvers map[string]ObligationStatusReader) error {
	if reg == nil {
		return nil
	}
	return reg.RegisterParameterized(ObligationGatePrefix, func(ec EvalContext) (bool, error) {
		kind := strings.TrimSpace(strings.TrimPrefix(ec.ConditionID, ObligationGatePrefix))
		if kind == "" {
			return false, nil
		}
		reader := resolvers[kind]
		if reader == nil {
			return false, nil
		}
		runID := strings.TrimSpace(ec.WorkflowRunID)
		if runID == "" {
			return false, nil
		}
		status, err := reader.Status(ec.Ctx, runID, ec.Phase)
		if err != nil {
			return false, err
		}
		return ObligationSettled(status, ec.Vars, kind), nil
	})
}

// ObligationSettled reports whether a kind's status releases its gate leaf.
// The gate evaluator and the host-hold fact share this definition.
func ObligationSettled(status api.WorkflowRunObligation, vars map[string]any, kind string) bool {
	switch status.Status {
	case api.ObligationStatusOff, api.ObligationStatusComplete, api.ObligationStatusFailed:
		return true
	case api.ObligationStatusEmpty:
		// Preserve failures that occur before a ledger row exists.
		return ObligationFailedInVars(vars, kind)
	default:
		return false
	}
}

// ObligationFailedInVars reads the enqueue-failure stamp under obligations.<kind>.
func ObligationFailedInVars(vars map[string]any, kind string) bool {
	obligations, ok := vars["obligations"].(map[string]any)
	if !ok {
		return false
	}
	summary, ok := obligations[kind].(map[string]any)
	if !ok {
		return false
	}
	status, _ := summary["status"].(string)
	return status == api.ObligationStatusFailed
}
