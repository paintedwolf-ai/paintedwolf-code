package definition_test

import (
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"testing"
)

func TestVerificationRequirementsDistinguishDeliveryFromTests(t *testing.T) {
	for _, tc := range []struct {
		expression string
		required   bool
	}{
		{"delivery_gates_passed", false},
		{"closeout_gates_passed", true},
		{"evidence_passed:verify", true},
		{"evidence_passed:test", true},
	} {
		t.Run(tc.expression, func(t *testing.T) {
			manifest := workflowdef.Manifest{Phases: []string{"work"}, PhaseDefs: []workflowdef.PhaseDef{{ID: "work", CompleteWhen: tc.expression}}}
			got := workflowdef.PhaseEvidenceRequirements(manifest, "work")
			if tc.required {
				if len(got) != 1 || got[0] != "verify" {
					t.Fatalf("requirements=%v", got)
				}
			} else if len(got) != 0 {
				t.Fatalf("delivery invented test requirements: %v", got)
			}
		})
	}
}
