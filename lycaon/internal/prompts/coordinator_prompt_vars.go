package prompts

import "strings"

// CoordinatorPromptGates are turn-scoped prompt conditions.
type CoordinatorPromptGates struct {
	PendingOverlayPromote bool
	// VerifyRequired reflects the active phase evidence gate.
	VerifyRequired bool
	// VerifyCommand is the project's declared test command, or empty when undeclared.
	VerifyCommand string
}

// MergeCoordinatorPromptVars adds ordered turn-surface variables. The offered
// set comes from MergeCoordinatorSurfacePathVars when it ran first; otherwise
// the surface floor is what this call offers.
func MergeCoordinatorPromptVars(
	surfaceID string,
	transition ExecutionModePromptTransition,
	gates CoordinatorPromptGates,
	into map[string]any,
) error {
	if into == nil {
		return nil
	}
	MergeCoordinatorTurnSurfaceVars(surfaceID, gates.PendingOverlayPromote, into)
	into["execution_mode"] = strings.TrimSpace(transition.ExecutionMode)
	into["execution_mode_previous"] = strings.TrimSpace(transition.ExecutionModePrevious)
	into["execution_mode_entered"] = strings.TrimSpace(transition.ExecutionModeEntered)
	into["execution_mode_left"] = strings.TrimSpace(transition.ExecutionModeLeft)
	offered, err := LoadCoordinatorSurfaceFloor(surfaceID)
	if err != nil {
		return err
	}
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
	cardLabel, cardRule, err := LoadCoordinatorSurfaceCard(surfaceID)
	if err != nil {
		return err
	}
	for k, v := range CoordinatorSurfaceCardVars(cardLabel, cardRule, offered, requestable).TemplateVars() {
		into[k] = v
	}
	MergeVisualShowVars(offered, into)
	into["more_tools_loadable"] = len(requestable) > 0
	into["verify_required"] = gates.VerifyRequired
	into["verify_command"] = strings.TrimSpace(gates.VerifyCommand)
	return nil
}

// ExecutionModePromptTransition carries execution-mode template data.
type ExecutionModePromptTransition struct {
	ExecutionMode         string
	ExecutionModePrevious string
	ExecutionModeEntered  string
	ExecutionModeLeft     string
}
