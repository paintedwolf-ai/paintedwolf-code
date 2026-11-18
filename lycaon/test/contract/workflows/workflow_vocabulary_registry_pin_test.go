package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/vocabulary"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWorkflowVocabularyMatchesRuntime(t *testing.T) {
	t.Parallel()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build condition registry", err)
	contractcheck.FailErr(t, "register rule conditions", rules.RegisterRuleConditions(reg))
	bound := map[string]bool{}
	for _, name := range reg.ExactNames() {
		bound[name] = true
	}
	for _, prefix := range reg.ParameterizedPrefixes() {
		bound[prefix+"*"] = true
	}
	seen := map[string]bool{}
	for _, row := range vocabulary.ExportCatalog() {
		if seen[row.ID] {
			t.Errorf("duplicate declaration %q", row.ID)
		}
		seen[row.ID] = true
		want := row.Status == "shipped" && row.ID != workflowdef.CompleteWhenGatesSatisfied && row.ID != workflowdef.CompleteWhenGateSatisfied+"*"
		if bound[row.ID] != want {
			t.Errorf("%s: evaluator registered = %t, want %t", row.ID, bound[row.ID], want)
		}
		delete(bound, row.ID)
	}
	for name := range bound {
		t.Errorf("evaluator %q has no catalog declaration", name)
	}
}
