package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWorkflowVocabularyDelegateLeavesRegistered(t *testing.T) {
	t.Parallel()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "NewDefaultRegistry", err)
	for _, id := range workflowdef.DelegateSubroutineGateLeafIDs() {
		if !reg.Has(id) {
			t.Fatalf("delegate/subroutine leaf %q not registered", id)
		}
		if !workflowdef.IsKnownGateLeaf(id) {
			t.Fatalf("leaf %q missing from workflow gate vocabulary", id)
		}
	}
}
