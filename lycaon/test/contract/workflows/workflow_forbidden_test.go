package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/vocabulary"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Each forbidden id is refused by catalog load wherever an author can write it:
// a phase complete_when and a posture rule when key.
func TestForbiddenIDsAreRejectedByRegistry(t *testing.T) {
	t.Parallel()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	for _, id := range []string{"stage_plan_complete", "verify_evidence_passed", "user_input_clarify", "request_user_input", "mode_is"} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			manifests := workflowdef.NewRegistry(map[string]workflowdef.Manifest{
				"forbidden@1.0.0": workflowdef.FinalizeManifest(workflowdef.Manifest{
					ID: "forbidden", Version: "1.0.0",
					PhaseDefs: []workflowdef.PhaseDef{{ID: "x", CompleteWhen: id}},
				}),
			})
			ruleConfig := &rules.RulesConfig{Rules: []rules.RuleEntry{{ID: "forbidden-rule", When: map[string]any{id: true}}}}
			diags := vocabulary.ValidateBundled(reg, manifests, []*rules.RulesConfig{ruleConfig})
			want := map[string]bool{
				"workflow[forbidden@1.0.0].phases[x].complete_when": false,
				"rules[0].forbidden-rule.when":                      false,
			}
			for _, d := range diags {
				if _, ok := want[d.Field]; ok && d.Code == "forbidden_predicate" {
					want[d.Field] = true
				}
			}
			for field, seen := range want {
				if !seen {
					t.Errorf("%s: no forbidden_predicate diagnostic; diagnostics = %+v", field, diags)
				}
			}
		})
	}
}
