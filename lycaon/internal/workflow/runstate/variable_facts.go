package runstate

import (
	"github.com/lycaon/lycaon/internal/conditions"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"time"
)

func SetHostVar(vars map[string]any, path string, value any) map[string]any {
	return conditions.SetDotPath(CloneVars(vars), path, value)
}

func CloneVars(vars map[string]any) map[string]any {
	if vars == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(vars))
	for k, v := range vars {
		out[k] = v
	}
	return out
}

func SetGateSatisfied(vars map[string]any, gate string, satisfied bool) map[string]any {
	vars = CloneVars(vars)
	gates, _ := vars["gates"].(map[string]any)
	if gates == nil {
		gates = map[string]any{}
		vars["gates"] = gates
	}
	gates[gate] = satisfied
	return vars
}

func SaveBaselinePosture(vars map[string]any, posture api.SessionPosture) map[string]any {
	vars = CloneVars(vars)
	vars = SetHostVar(vars, BaselinePostureKey, string(posture))
	return vars
}

func CompleteTerminalPhaseEntry(run *api.WorkflowRun, def workflowdef.PhaseDef, now time.Time) bool {
	if run == nil || !def.Terminal {
		return false
	}
	run.Status = api.WorkflowRunStatusComplete
	run.CompletedAt = &now
	return true
}

func RunHasBlueprint(run *api.WorkflowRun) bool {
	return run != nil && strings.TrimSpace(run.BlueprintPath) != ""
}

func ApplyPhaseContentReviewVars(vars map[string]any, def workflowdef.PhaseDef) map[string]any {
	vars = CloneVars(vars)
	if def.ContentReview == nil {
		delete(vars, "content_review")
		return vars
	}
	vars["content_review"] = map[string]any{
		"tools": append([]string(nil), def.ContentReview.Tools...),
		"paths": append([]string(nil), def.ContentReview.Paths...),
	}
	return vars
}
