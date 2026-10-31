package prompts

import (
	"slices"
	"strings"
)

// MergeCoordinatorPromptVars adds ordered turn-surface variables. The offered
// set comes from MergeCoordinatorSurfacePathVars when it ran first; otherwise
// the surface floor is what this call offers.
func MergeCoordinatorPromptVars(
	surfaceID string,
	transition ExecutionModePromptTransition,
	verifyCommand string,
	into map[string]any,
) error {
	if into == nil {
		return nil
	}
	into["surface_id"] = strings.TrimSpace(surfaceID)
	into["execution_mode"] = strings.TrimSpace(transition.ExecutionMode)
	into["execution_mode_previous"] = strings.TrimSpace(transition.ExecutionModePrevious)
	into["execution_mode_entered"] = strings.TrimSpace(transition.ExecutionModeEntered)
	into["execution_mode_left"] = strings.TrimSpace(transition.ExecutionModeLeft)
	floor, err := LoadCoordinatorSurfaceFloor(surfaceID)
	if err != nil {
		return err
	}
	offered := floor
	if set, ok := into["surface_offered"].([]string); ok {
		offered = set
	}
	loadable, err := LoadCoordinatorSurfaceLoadable(surfaceID)
	if err != nil {
		return err
	}
	offeredSet := toolNameSet(offered)
	requestable := make([]string, 0, len(loadable))
	for _, name := range loadable {
		if !offeredSet[name] {
			requestable = append(requestable, name)
		}
	}
	into["is_coordinator"] = true
	into["command_on_floor"] = slices.Contains(floor, "command")
	ApplyCoordinatorSurfaceCommandEffects(surfaceID, offered, requestable, into)
	cardLabel, cardRule, err := LoadCoordinatorSurfaceCard(surfaceID)
	if err != nil {
		return err
	}
	runOffered := offered
	if !CoordinatorSurfaceAllowsCommandMutation(surfaceID) {
		runOffered = filterToolName(offered, "command")
	}
	for k, v := range CoordinatorSurfaceCardVars(cardLabel, cardRule, runOffered, requestable).TemplateVars() {
		into[k] = v
	}
	MergeVisualShowVars(runOffered, requestable, into)
	into["more_tools_loadable"] = len(requestable) > 0
	into["verify_command"] = strings.TrimSpace(verifyCommand)
	return nil
}

// ExecutionModePromptTransition carries execution-mode template data.
type ExecutionModePromptTransition struct {
	ExecutionMode         string
	ExecutionModePrevious string
	ExecutionModeEntered  string
	ExecutionModeLeft     string
}
