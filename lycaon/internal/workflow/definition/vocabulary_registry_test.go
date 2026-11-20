package definition

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStaticVocabularyMatchesConditionRegistry(t *testing.T) {
	t.Parallel()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	for id := range registeredCompleteWhenPredicates {
		if !reg.Has(id) {
			t.Errorf("complete_when predicate %q is in the static table but not registered as a condition evaluator", id)
		}
	}
	for id := range registeredGateLeafIDs {
		if !reg.Has(id) {
			t.Errorf("gate leaf %q is in the static table but not registered as a condition evaluator", id)
		}
	}
}
