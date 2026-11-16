package prompts

import "strings"

// MergeCoordinatorPromptVars adds turn-occurrence transition and verification variables.
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
