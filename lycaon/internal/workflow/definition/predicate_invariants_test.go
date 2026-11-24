package definition

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/vocabulary"
)

func TestEveryPhasePredicateFieldUsesTheDeclaredVocabulary(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		overlay    bool
	}{
		{"undeclared", "unknown_predicate", false},
		{"findings_triaged", "catalog_only_predicate", false},
		{"var_set:x", "forbidden_predicate", false},
		{"plan_stub_valid", "domain_leaf_on_overlay", true},
		{"evidence_passed:verify", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expr := "phase_is:build or not " + tc.name
			phase := PhaseDef{
				CompleteWhen: "gate_satisfied:" + tc.name, ChildCompleteWhen: expr,
				Gates: []string{tc.name}, ChildGates: []string{tc.name}, EntryWhen: expr,
				Transitions:   []PhaseTransitionDef{{ID: "finish", When: expr}},
				HumanApproval: &HumanApprovalConfig{Readiness: expr},
			}
			got := phase.PredicateDiagnostics("phase", vocabulary.IsPredicate, tc.overlay)
			if tc.code == "" {
				if len(got) > 0 {
					t.Fatalf("allowed leaf rejected: %+v", got)
				}
				return
			}
			fields := map[string]bool{}
			for _, d := range got {
				code := tc.code
				if strings.HasSuffix(d.Field, ".readiness") && code == "unknown_predicate" {
					code = "unknown_readiness"
				}
				if d.Code != code {
					t.Errorf("%s code = %s, want %s", d.Field, d.Code, code)
				}
				fields[d.Field] = true
			}
			for _, field := range []string{"complete_when", "child_complete_when", "gates[0]", "child_gates[0]", "entry_when", "transitions[finish].when", "human_approval.readiness"} {
				if !fields["phase."+field] {
					t.Errorf("missing diagnostic for %s", field)
				}
			}
		})
	}
}
