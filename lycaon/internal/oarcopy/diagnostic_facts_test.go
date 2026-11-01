package oarcopy

import (
	"encoding/json"
	"math"
	"testing"
)

func TestDiagnosticKnowledgeRequiresIntegralProducerObservations(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		known bool
	}{
		{"zero", 0, true}, {"integer", 42, true}, {"wide integer", int64(42), true},
		{"decoded zero", float64(0), true}, {"decoded integer", float64(42), true},
		{"JSON integer", json.Number("42"), true},
		{"missing", nil, false}, {"boolean", true, false}, {"text", "42", false},
		{"negative", -1, false}, {"negative wide", int64(-1), false},
		{"fraction", 0.5, false}, {"negative decoded", float64(-1), false},
		{"NaN", math.NaN(), false}, {"infinity", math.Inf(1), false},
		{"overflow", math.Exp2(63), false}, {"JSON fraction", json.Number("1.5"), false},
		{"JSON negative", json.Number("-1"), false}, {"JSON overflow", json.Number("9223372036854775808"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := map[string]any{}
			for _, key := range []string{"maximum", "observed", "max", "background_limit", "count", "max_awaited"} {
				data[key] = tc.value
			}
			facts := map[string]any{}
			PublishDiagnosticKnowledge(facts, data)
			for _, key := range []string{"limit_observation_known", "glob_limit_known", "background_limit_known", "awaited_capacity_known", "failure_count_known"} {
				if facts["paintedwolf."+key] != tc.known {
					t.Errorf("%s = %v, want %t", key, facts["paintedwolf."+key], tc.known)
				}
			}
		})
	}
}
