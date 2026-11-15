package prompts

import "strings"

// MergeCoordinatorPromptVars adds turn-occurrence variables and ensures reader
// facts are present when called standalone.
func MergeCoordinatorPromptVars(
	surfaceID string,
	transition ExecutionModePromptTransition,
	verifyCommand string,
	into map[string]any,
) error {
	if into == nil {
		return nil
	}
	if _, err := loadCoordinatorSurfaceRow(surfaceID); err != nil {
		return err
	}
	if _, hasCard := into["surface_card"]; !hasCard {
		var loaded map[string]bool
		if set, ok := into["surface_offered"].([]string); ok {
			loaded = toolNameSet(set)
		}
		if err := MergeCoordinatorSurfacePathVars(surfaceID, nil, into, ReaderInput{
			Mode:   strings.TrimSpace(transition.ExecutionMode),
			Loaded: loaded,
		}); err != nil {
			return err
		}
	}
	into["surface_id"] = strings.TrimSpace(surfaceID)
	into["execution_mode"] = strings.TrimSpace(transition.ExecutionMode)
	into["execution_mode_previous"] = strings.TrimSpace(transition.ExecutionModePrevious)
	into["execution_mode_entered"] = strings.TrimSpace(transition.ExecutionModeEntered)
	into["execution_mode_left"] = strings.TrimSpace(transition.ExecutionModeLeft)
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
