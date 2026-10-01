package workflow

import (
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
)

// SetBoardOrientReadyVar stamps the board orientation gate.
func SetBoardOrientReadyVar(vars map[string]any, injectKey string) map[string]any {
	vars = cloneVars(vars)
	board := map[string]any{"orient_ready": true}
	injectKey = strings.TrimSpace(injectKey)
	if injectKey != "" {
		board["inject_key"] = injectKey
	}
	vars["board"] = board
	return vars
}

// SetWorkerCycleEvalVars scopes the worker-cycle gate evaluation.
func SetWorkerCycleEvalVars(vars map[string]any, completingJobID, summaryStatus string) map[string]any {
	vars = cloneVars(vars)
	vars["worker_cycle"] = map[string]any{
		"evaluating":        true,
		"completing_job_id": strings.TrimSpace(completingJobID),
		"summary_status":    strings.TrimSpace(summaryStatus),
	}
	return vars
}

// ClearWorkerCycleEvalVars clears the worker-cycle gate scope.
func ClearWorkerCycleEvalVars(vars map[string]any) map[string]any {
	vars = cloneVars(vars)
	delete(vars, "worker_cycle")
	return vars
}

// SetChildRunStatusVar stamps child status for subroutine gates.
func SetChildRunStatusVar(vars map[string]any, status string) map[string]any {
	return conditions.SetDotPath(cloneVars(vars), "child_run.status", strings.TrimSpace(status))
}
