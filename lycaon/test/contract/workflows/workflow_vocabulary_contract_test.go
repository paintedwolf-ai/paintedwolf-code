package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil/conditionstest"
	"github.com/lycaon/lycaon/internal/vocabulary"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWorkflowVocabularyDelegateLeavesRegistered(t *testing.T) {
	t.Parallel()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "NewDefaultRegistry", err)
	for _, id := range conditionstest.DelegateAndSubroutineIDs {
		if !reg.Has(id) {
			t.Fatalf("delegate/subroutine leaf %q not registered", id)
		}
		if !vocabulary.IsPredicate(id) {
			t.Fatalf("leaf %q missing from workflow gate vocabulary", id)
		}
	}
}
