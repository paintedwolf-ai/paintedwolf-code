package runstate

import (
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func ApplyAutoApproveEffects(vars map[string]any, manifest workflowdef.Manifest) map[string]any {
	if !ParameterIsTrue(vars, "auto_approve") {
		return vars
	}
	vars = SetHostVar(vars, "phase_skipped.approve", true)
	if bucket, ok := vars["user_feedback"].(map[string]any); ok {
		for _, def := range manifest.PhaseDefs {
			if len(def.Intake) > 0 {
				delete(bucket, def.ID)
			}
		}
	}
	for _, def := range manifest.PhaseDefs {
		if len(def.Intake) == 0 {
			continue
		}
		for _, key := range def.Intake {
			choice := AutoApproveIntakeDefault(key)
			vars = SetDecisionChoice(vars, key, choice, "")
			vars = SetHostVar(vars, "intake."+key, choice)
		}
	}
	// Stamp consulted gates for auto-approved phases.
	for _, def := range manifest.PhaseDefs {
		for _, g := range def.Gates {
			if strings.HasPrefix(g, "hitl_consulted:") {
				vars = StampHitlConsulted(vars, def.ID)
				break
			}
		}
	}
	return vars
}

func ApplyMergedParams(vars map[string]any, params map[string]string) map[string]any {
	vars = CloneVars(vars)
	for name, val := range params {
		if val != "" {
			vars = SetHostVar(vars, "params."+name, val)
		}
	}
	return vars
}

func RecordWorkflowArtifactParams(vars map[string]any, manifest workflowdef.Manifest) map[string]any {
	if manifest.Blueprint == nil {
		return vars
	}
	for name, spec := range manifest.Parameters {
		if spec.Type != "depth" {
			continue
		}
		if val, ok := DotPathString(vars, "params."+name); ok {
			vars = SetHostVar(vars, "artifact."+manifest.Blueprint.ID+"."+name, val)
		}
	}
	return vars
}

func StampDepthParamSkips(vars map[string]any, manifest workflowdef.Manifest) map[string]any {
	for _, def := range manifest.PhaseDefs {
		if def.DepthParam == "" {
			continue
		}
		vars = ResolveDepthSkip(vars, def.ID, def.DepthParam)
		if def.ReviewLoop != nil && conditions.DotPathTruthy(vars, "phase_skipped."+def.ID) {
			key := strings.TrimSpace(def.ReviewLoop.EvidenceKey)
			if key != "" {
				vars = SetGateSatisfied(vars, "evidence_passed:"+key, true)
			}
		}
	}
	return vars
}

func DotPathString(vars map[string]any, path string) (string, bool) {
	v, ok := conditions.DotPathGet(vars, path)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return strings.TrimSpace(s), ok && s != ""
}

func ResolveDepthSkip(vars map[string]any, phaseID, paramName string) map[string]any {
	level, err := DepthParamLevel(vars, paramName)
	if err != nil || level != workflowdef.DepthNone {
		return vars
	}
	vars = SetHostVar(vars, "phase_skipped."+phaseID, true)
	return vars
}

func DepthParamLevel(vars map[string]any, paramName string) (workflowdef.DepthLevel, error) {
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

func AutoApproveIntakeDefault(key string) string {
	switch strings.TrimSpace(key) {
	case "change_size":
		return "medium"
	case "breaking_change":
		return "none"
	default:
		return "medium"
	}
}

func ParameterIsTrue(vars map[string]any, name string) bool {
	raw, ok := DotPathString(vars, "params."+name)
	return ok && strings.EqualFold(strings.TrimSpace(raw), "true")
}
