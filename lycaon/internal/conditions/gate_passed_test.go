package conditions_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
)

// gate_passed:<name> reads only the typed gates map; other vars never satisfy it.
func TestGatePassedReadsOnlyTheGatesVar(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "build conditions registry", err)
	prose := "Advisory summary recommends merging this approach."
	cases := []struct {
		name string
		vars map[string]any
		want bool
	}{
		{"gate set", map[string]any{"gates": map[string]any{"verify": true}}, true},
		{"gate set with prose", map[string]any{"advisory_summary_text": prose, "gates": map[string]any{"verify": true}}, true},
		{"gate false with prose", map[string]any{"advisory_summary_text": prose, "gates": map[string]any{"verify": false}}, false},
		{"other gate set", map[string]any{"gates": map[string]any{"research": true}}, false},
		{"prose only", map[string]any{"advisory_summary_text": prose, "verify": true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ec := conditions.EvalContext{ConditionID: "gate_passed:verify", Vars: tc.vars}
			got, err := reg.Evaluate("gate_passed:verify", ec)
			testutil.FailErr(t, "evaluate gate_passed:verify", err)
			if got != tc.want {
				t.Fatalf("gate_passed:verify = %v, want %v", got, tc.want)
			}
		})
	}
}
