package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCoordinatorFlowTable_loadAndValidate(t *testing.T) {
	t.Parallel()
	table, err := surface.LoadCoordinatorFlow()
	contractcheck.FailErr(t, "LoadCoordinatorFlow", err)
	if table.HitPolicy != "first" {
		t.Fatalf("hit_policy = %q want first", table.HitPolicy)
	}
	defaultSurface := table.Default.SurfaceID
	if defaultSurface == "" {
		defaultSurface = table.Default.Default
	}
	if defaultSurface != "implement_investigate" {
		t.Fatalf("default surface = %q want implement_investigate", defaultSurface)
	}
	for _, name := range surface.RegisteredSurfaceFactNames() {
		if !surface.IsKnownFact(name) {
			t.Fatalf("registry fact %q not known", name)
		}
	}
}
