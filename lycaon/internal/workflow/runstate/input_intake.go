package runstate

import (
	"fmt"
	"slices"
	"strings"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowintake "github.com/lycaon/lycaon/internal/workflow/intake"
)

func NextPendingIntakeKey(def workflowdef.PhaseDef, vars map[string]any) (string, bool) {
	for _, key := range def.Intake {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if !IntakeDecisionReceived(vars, key) {
			return key, true
		}
	}
	return "", false
}

func IntakeDecisionReceived(vars map[string]any, key string) bool {
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

func PendingIntakeKey(def workflowdef.PhaseDef, vars map[string]any) (string, bool) {
	if len(def.Intake) == 0 {
		return "", false
	}
	if len(def.Intake) == 1 {
		return strings.TrimSpace(def.Intake[0]), true
	}
	return NextPendingIntakeKey(def, vars)
}

func IntakeContainsKey(def workflowdef.PhaseDef, key string) bool {
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

func LatchIntakeKey(vars map[string]any, key string) (map[string]any, error) {
	catalog, err := workflowintake.Bundled()
	if err != nil {
		return vars, err
	}
	q, ok := catalog.Get(key)
	if !ok {
		return vars, fmt.Errorf("unknown intake key %q", key)
	}
	vars = CloneAskVars(vars)
	options := append([]string(nil), q.Options...)
	if len(options) > 0 {
		return SetDecisionPending(vars, key, q.Question, options), nil
	}
	return SetFeedbackPending(vars, key, q.Question), nil
}

func SetIntakeDecision(vars map[string]any, key, choice string) map[string]any {
	vars = SetHostVar(vars, "intake."+key, choice)
	switch key {
	case "change_size":
		vars = SetHostVar(vars, "artifact.plan.scope", choice)
	case "breaking_change":
		vars = SetHostVar(vars, "artifact.plan.breaking", choice)
	}
	return SetDecisionChoice(vars, key, choice, "")
}

func ApplyIntakeResponse(vars map[string]any, def workflowdef.PhaseDef, phaseID, response string) (map[string]any, error) {
	key, ok := PendingIntakeKey(def, vars)
	if !ok {
		return nil, fmt.Errorf("phase %q has no pending intake key", phaseID)
	}
	catalog, err := workflowintake.Bundled()
	if err != nil {
		return nil, err
	}
	q, ok := catalog.Get(key)
	if !ok {
		return nil, fmt.Errorf("unknown intake key %q", key)
	}
	response = strings.TrimSpace(response)
	if !slices.Contains(q.Options, response) {
		return nil, fmt.Errorf("intake response %q is not an option for key %q", response, key)
	}
	vars = SetIntakeDecision(vars, key, response)
	if nextKey, more := NextPendingIntakeKey(def, vars); more {
		vars, err = LatchIntakeKey(vars, nextKey)
		if err != nil {
			return nil, err
		}
	}
	return vars, nil
}
