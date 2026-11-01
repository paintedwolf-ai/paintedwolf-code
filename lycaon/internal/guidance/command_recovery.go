package guidance

import (
	"encoding/json"
	"math"
)

// commandRecoveryView exposes consistent producer-stated execution observations.
func commandRecoveryView(data map[string]any) map[string]any {
	tool, _ := data["tool"].(string)
	if tool != "command" && tool != "verify" {
		return nil
	}
	state, _ := data["command_execution"].(string)
	counts := map[string]int64{}
	present := false
	for _, key := range []string{"program_completed_effects", "program_uncertain_effects", "program_executed_children"} {
		value, found := data[key]
		if !found {
			continue
		}
		n, ok := recoveryCount(value)
		if !ok {
			return nil
		}
		present = true
		counts[key] = n
	}
	switch state {
	case "not_started":
		for _, n := range counts {
			if n != 0 {
				return nil
			}
		}
		top, _ := data["diagnostic_top_level"].(bool)
		stage, _ := data["diagnostic_stage"].(string)
		if (top && stage == "syntax") || (present && len(counts) == 3) {
			return map[string]any{"not_started": true}
		}
	case "observed", "active":
		if len(counts) == 3 {
			return map[string]any{"observed": state == "observed", "active": state == "active", "completed": counts["program_completed_effects"], "uncertain": counts["program_uncertain_effects"], "started": counts["program_executed_children"]}
		}
	}
	return nil
}

func recoveryCount(value any) (int64, bool) {
	switch n := value.(type) {
	case int:
		return int64(n), n >= 0
	case int64:
		return n, n >= 0
	case float64:
		if n >= 0 && n < math.Exp2(63) && math.Trunc(n) == n {
			return int64(n), true
		}
	case json.Number:
		parsed, err := n.Int64()
		return parsed, err == nil && parsed >= 0
	}
	return 0, false
}
