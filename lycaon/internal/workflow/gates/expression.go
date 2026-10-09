package gates

import (
	"context"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

type ExpressionEvaluator interface {
	ExpressionMet(context.Context, string, workflowdef.PhaseDef, *api.WorkflowRun, map[string]any) (bool, GateCheckResult, error)
}
