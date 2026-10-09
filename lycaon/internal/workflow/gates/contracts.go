package gates

import (
	"context"
	"strings"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

type GateCheckResult struct {
	Reason       string
	FailedGate   string
	FailedLeaves []string
}
type GateEvaluator interface {
	PhaseGateMet(ctx context.Context, manifest workflowdef.Manifest, run *api.WorkflowRun, vars map[string]any) (bool, GateCheckResult, error)
}

func SatisfiedInVars(vars map[string]any, gate string) bool {
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
