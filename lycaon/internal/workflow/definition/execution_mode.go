package definition

import (
	"fmt"
	"strings"
)

const (
	ExecutionModeInvestigate  = "investigate"
	ExecutionModeOrchestrate  = "orchestrate"
	ExecutionModeStateDerived = "state_derived"

	ScaffoldExecutionModeVar = "execution_mode"
)

// ValidExecutionMode reports whether value is a known execution-mode YAML token.
func ValidExecutionMode(value string) bool {
	switch NormalizeExecutionMode(value) {
	case ExecutionModeInvestigate, ExecutionModeOrchestrate, ExecutionModeStateDerived, "":
		return NormalizeExecutionMode(value) != ""
	default:
		return false
	}
}

// NormalizeExecutionMode trims and lowercases execution-mode YAML tokens.
func NormalizeExecutionMode(value string) string {
	return strings.TrimSpace(strings.ToLower(value))
}

// IsForceExecutionMode reports investigate | orchestrate (not state_derived / empty).
func IsForceExecutionMode(value string) bool {
	switch NormalizeExecutionMode(value) {
	case ExecutionModeInvestigate, ExecutionModeOrchestrate:
		return true
	default:
		return false
	}
}

// ForceExecutionMode returns investigate | orchestrate or "".
func ForceExecutionMode(value string) string {
	mode := NormalizeExecutionMode(value)
	if IsForceExecutionMode(mode) {
		return mode
	}
	return ""
}

func ValidateExecutionModeField(field, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if !ValidExecutionMode(value) {
		return fmt.Errorf("%s: invalid execution mode %q (want investigate, orchestrate, or state_derived)", field, value)
	}
	return nil
}

// ScaffoldExecutionModeStamp reads the phase's execution-mode stamp from run scaffold vars.
func ScaffoldExecutionModeStamp(vars map[string]any) (string, bool) {
	if vars == nil {
		return "", false
	}
	raw, ok := vars[ScaffoldExecutionModeVar]
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	if !ok {
		return "", false
	}
	mode := ForceExecutionMode(s)
	return mode, mode != ""
}

// WorkflowDefaultForceMode resolves the workflow's default execution mode when no phase stamp is present.
func WorkflowDefaultForceMode(m Manifest, vars map[string]any) (string, bool) {
	if _, ok := ScaffoldExecutionModeStamp(vars); ok {
		return "", false
	}
	mode := ForceExecutionMode(m.Controls.DefaultExecutionMode)
	return mode, mode != ""
}
