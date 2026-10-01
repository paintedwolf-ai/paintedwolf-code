package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestStaticVocabularyMatchesConditionRegistry pins the workflow package's static,
// registry-independent vocabulary tables (used to validate manifests at load time)
// to the real runtime condition registry. A predicate or gate leaf listed in the
// static table but absent from the evaluator registry would pass load-time
// validation and then fail at runtime.
func TestStaticVocabularyMatchesConditionRegistry(t *testing.T) {
	t.Parallel()
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	contractcheck.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	for _, id := range workflowdef.RegisteredCompleteWhenPredicates() {
		if !reg.Has(id) {
			t.Errorf("complete_when predicate %q is in the static table but not registered as a condition evaluator", id)
		}
	}
	for _, id := range workflowdef.RegisteredGateLeafIDs() {
		if !reg.Has(id) {
			t.Errorf("gate leaf %q is in the static table but not registered as a condition evaluator", id)
		}
	}
}
