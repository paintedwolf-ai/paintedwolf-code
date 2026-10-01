package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/extpacks"
)

// Contribution kinds and inventory roots form one closed vocabulary.
func TestContributionKindsMatchInventoryRoots(t *testing.T) {
	t.Parallel()

	roots := extpacks.ContributionKindRoots()
	kinds := contribution.Kinds()
	if len(roots) != len(kinds) {
		t.Fatalf("inventory roots %v vs kinds %v", roots, kinds)
	}
	rootSet := map[string]bool{}
	for _, root := range roots {
		rootSet[root] = true
	}
	for _, kind := range kinds {
		if !rootSet[kind.UnitRoot()] {
			t.Fatalf("kind %s has no inventory root %s in extpacks", kind, kind.UnitRoot())
		}
		if got, ok := contribution.KindForUnitRoot(kind.UnitRoot()); !ok || got != kind {
			t.Fatalf("kind %s does not round-trip its unit root", kind)
		}
	}
	for _, root := range roots {
		if extpacks.ProjectScope(root) != extpacks.ScopeDeviceOnly {
			t.Fatalf("root %s must be device-only", root)
		}
	}
}
