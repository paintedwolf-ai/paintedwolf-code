package workflow

import (
	"fmt"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// stampExecutionModeVar applies a phase's on_enter.set_execution_mode to scaffold vars;
// state_derived clears the stamp.
func stampExecutionModeVar(vars map[string]any, phaseID, mode string) (map[string]any, error) {
	mode = workflowdef.NormalizeExecutionMode(mode)
	if mode == "" {
		return vars, nil
	}
	if err := workflowdef.ValidateExecutionModeField("on_enter.set_execution_mode", mode); err != nil {
		return nil, fmt.Errorf("phase %q: %w", phaseID, err)
	}
	vars = cloneVars(vars)
	if mode == workflowdef.ExecutionModeStateDerived {
		delete(vars, workflowdef.ScaffoldExecutionModeVar)
		return vars, nil
	}
	vars[workflowdef.ScaffoldExecutionModeVar] = mode
	return vars, nil
}
