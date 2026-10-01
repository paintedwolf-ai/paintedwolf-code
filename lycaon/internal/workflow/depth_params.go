package workflow

import (
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// DepthFanOutCount maps depth to parallel fan-out width.
func DepthFanOutCount(level workflowdef.DepthLevel) int {
	switch level {
	case workflowdef.DepthNone:
		return 0
	case workflowdef.DepthThorough:
		return 3
	default:
		return 1
	}
}

// ResolveDepthSkip stamps phase_skipped when a depth param resolves to none.
func ResolveDepthSkip(vars map[string]any, phaseID, paramName string) map[string]any {
	level, err := depthParamLevel(vars, paramName)
	if err != nil || level != workflowdef.DepthNone {
		return vars
	}
	vars = SetHostVar(vars, "phase_skipped."+phaseID, true)
	return vars
}

func depthParamLevel(vars map[string]any, paramName string) (workflowdef.DepthLevel, error) {
	paramName = strings.TrimSpace(paramName)
	if paramName == "" {
		return workflowdef.DepthLight, nil
	}
	path := "params." + paramName
	raw, ok := DotPathString(vars, path)
	if !ok {
		return workflowdef.DepthLight, nil
	}
	return workflowdef.ParseDepthLevel(raw)
}

// DotPathString reads a workflow variable at a dotted path, empty meaning absent.
func DotPathString(vars map[string]any, path string) (string, bool) {
	v, ok := conditions.DotPathGet(vars, path)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return strings.TrimSpace(s), ok && s != ""
}
