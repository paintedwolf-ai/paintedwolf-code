package phases

import (
	"fmt"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

// runstate.LatchIntakeKey writes pending input for one catalog key.

func applyMultiIntakeOnEnter(def workflowdef.PhaseDef, enter workflowdef.PhaseOnEnter, phaseID string, vars map[string]any) (workflowdef.PhaseOnEnter, map[string]any, error) {
	if len(def.Intake) == 0 {
		return enter, vars, nil
	}
	if runstate.ParameterIsTrue(vars, "auto_approve") {
		return enter, vars, nil
	}
	key, ok := runstate.NextPendingIntakeKey(def, vars)
	if !ok {
		return enter, vars, nil
	}
	// Key-scoped latches preserve per-question markers.
	vars, err := runstate.LatchIntakeKey(vars, key)
	if err != nil {
		return enter, vars, fmt.Errorf("phase %q: %w", phaseID, err)
	}
	return enter, vars, nil
}
