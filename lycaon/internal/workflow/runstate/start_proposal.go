package runstate

import (
	"strings"
)

func ClearWorkflowStartState(vars map[string]any) map[string]any {
	vars = CloneVars(vars)
	delete(vars, sessionVarWorkflowStartProposed)
	delete(vars, sessionVarPendingBlueprintLaunchPath)
	return vars
}

func SetPendingBlueprintLaunchPath(vars map[string]any, path string) map[string]any {
	vars = CloneVars(vars)
	path = strings.TrimSpace(path)
	if path == "" {
		delete(vars, sessionVarPendingBlueprintLaunchPath)
		return vars
	}
	vars[sessionVarPendingBlueprintLaunchPath] = path
	return vars
}

func PendingBlueprintLaunchPath(vars map[string]any) string {
	if vars == nil {
		return ""
	}
	path, _ := vars[sessionVarPendingBlueprintLaunchPath].(string)
	return strings.TrimSpace(path)
}

func SetWorkflowStartProposal(vars map[string]any, workflowID, version, presetID string) map[string]any {
	vars = CloneVars(vars)
	raw := map[string]any{
		"workflow_id":      strings.TrimSpace(workflowID),
		"workflow_version": strings.TrimSpace(version),
	}
	if strings.TrimSpace(presetID) != "" {
		raw["preset_id"] = strings.TrimSpace(presetID)
	}
	vars[sessionVarWorkflowStartProposed] = raw
	return vars
}

func ProposedWorkflow(vars map[string]any) (workflowID, version string, ok bool) {
	if vars == nil {
		return "", "", false
	}
	raw, _ := vars[sessionVarWorkflowStartProposed].(map[string]any)
	if raw == nil {
		return "", "", false
	}
	workflowID, _ = raw["workflow_id"].(string)
	version, _ = raw["workflow_version"].(string)
	workflowID = strings.TrimSpace(workflowID)
	version = strings.TrimSpace(version)
	if workflowID == "" || version == "" {
		return "", "", false
	}
	return workflowID, version, true
}

const (
	sessionVarWorkflowStartProposed      = "workflow_start_proposed"
	sessionVarPendingBlueprintLaunchPath = "pending_blueprint_launch_path"
)

func ProposedPresetID(vars map[string]any) string {
	raw, _ := vars[sessionVarWorkflowStartProposed].(map[string]any)
	if raw == nil {
		return ""
	}
	id, _ := raw["preset_id"].(string)
	return strings.TrimSpace(id)
}
