package oarcopy

import (
	"encoding/json"
	"math"
)

// PublishDiagnosticKnowledge distinguishes measured zeroes from unreported values.
func PublishDiagnosticKnowledge(facts, data map[string]any) {
	expected, _ := data["expected_revision"].(string)
	observed, _ := data["observed_revision"].(string)
	facts["paintedwolf.revision_change_observed"] = expected != "" && observed != "" && expected != observed
	facts["paintedwolf.limit_observation_known"] = observedCount(data, "maximum") && observedCount(data, "observed")
	facts["paintedwolf.glob_limit_known"] = observedCount(data, "max")
	facts["paintedwolf.background_limit_known"] = observedCount(data, "background_limit")
	facts["paintedwolf.awaited_capacity_known"] = observedCount(data, "count") && observedCount(data, "max_awaited")
	facts["paintedwolf.failure_count_known"] = observedCount(data, "count")
}

func observedCount(data map[string]any, key string) bool {
	_, known := CountObservation(data[key])
	return known
}

// CountObservation accepts only finite, nonnegative integral producer values.
func CountObservation(value any) (int64, bool) {
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
