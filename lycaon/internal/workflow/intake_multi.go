package workflow

import (
	"fmt"
	"strings"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func nextPendingIntakeKey(def workflowdef.PhaseDef, vars map[string]any) (string, bool) {
	for _, key := range def.Intake {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if !intakeDecisionReceived(vars, key) {
			return key, true
		}
	}
	return "", false
}

func intakeDecisionReceived(vars map[string]any, key string) bool {
	bucket, _ := vars["user_decision"].(map[string]any)
	if bucket == nil {
		return false
	}
	entry, ok := bucket[key].(map[string]any)
	if !ok {
		return false
	}
	choice, _ := entry["choice"].(string)
	return strings.TrimSpace(choice) != ""
}

func pendingIntakeKey(def workflowdef.PhaseDef, vars map[string]any) (string, bool) {
	if len(def.Intake) == 0 {
		return "", false
	}
	if len(def.Intake) == 1 {
		return strings.TrimSpace(def.Intake[0]), true
	}
	return nextPendingIntakeKey(def, vars)
}

func intakeContainsKey(def workflowdef.PhaseDef, key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	for _, k := range def.Intake {
		if strings.TrimSpace(k) == key {
			return true
		}
	}
	return false
}

// latchIntakeKey writes pending input for one catalog key.
func latchIntakeKey(vars map[string]any, key string) (map[string]any, error) {
	catalog, err := bundledIntakeCatalog()
	if err != nil {
		return vars, err
	}
	q, ok := catalog.Get(key)
	if !ok {
		return vars, fmt.Errorf("unknown intake key %q", key)
	}
	vars = cloneAskVars(vars)
	options := append([]string(nil), q.Options...)
	if len(options) > 0 {
		return setDecisionPending(vars, key, q.Question, options), nil
	}
	return setFeedbackPending(vars, key, q.Question), nil
}

func applyMultiIntakeOnEnter(def workflowdef.PhaseDef, enter workflowdef.PhaseOnEnter, phaseID string, vars map[string]any) (workflowdef.PhaseOnEnter, map[string]any, error) {
	if len(def.Intake) == 0 {
		return enter, vars, nil
	}
	if parameterIsTrue(vars, "auto_approve") {
		return enter, vars, nil
	}
	key, ok := nextPendingIntakeKey(def, vars)
	if !ok {
		return enter, vars, nil
	}
	// Key-scoped latches preserve per-question markers.
	vars, err := latchIntakeKey(vars, key)
	if err != nil {
		return enter, vars, fmt.Errorf("phase %q: %w", phaseID, err)
	}
	return enter, vars, nil
}
