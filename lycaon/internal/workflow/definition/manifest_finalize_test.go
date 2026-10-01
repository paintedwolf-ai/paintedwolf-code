package definition

import (
	"slices"
	"testing"
)

func TestFinalizedPhaseOrderContainsRetainedDefinitions(t *testing.T) {
	manifest := FinalizeManifest(Manifest{PhaseDefs: []PhaseDef{
		{ID: "done", ActivityLabel: "Done", Terminal: true},
		{ID: "unreachable", ActivityLabel: "Unreachable", Next: "missing"},
	}})
	if !slices.Equal(manifest.Phases, []string{"done"}) {
		t.Fatalf("phase order = %v, want retained terminal phase", manifest.Phases)
	}
	for _, id := range manifest.Phases {
		if _, ok := manifest.PhaseByID(id); !ok {
			t.Fatalf("ordered phase %q has no definition", id)
		}
	}
	if len(manifest.PhaseDefs) != 1 {
		t.Fatalf("retained definitions = %+v", manifest.PhaseDefs)
	}
}
