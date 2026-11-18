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

func TestWorkflowVocabularyExportListsMatchConditions(t *testing.T) {
	t.Parallel()
	exported := workflowdef.DelegateSubroutineGateLeafIDs()
	if len(exported) != len(conditions.ShippedDelegateLeafIDs())+len(conditions.ShippedSubroutineLeafIDs()) {
		t.Fatalf("export len = %d", len(exported))
	}
	for _, id := range conditions.ShippedDelegateLeafIDs() {
		found := false
		for _, s := range exported {
			if s == id {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing delegate leaf %q in export", id)
		}
	}
	for _, id := range conditions.ShippedSubroutineLeafIDs() {
		found := false
		for _, s := range exported {
			if s == id {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing subroutine leaf %q in export", id)
		}
	}
}
