package contract

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// A review_loop phase spawns its reviewer through task, so the coordinator
// surface it binds must offer task immediately.
func TestReviewLoopDeliberationPhasesBindAdjudicateSurface(t *testing.T) {
	t.Parallel()
	reg, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "RegistryFromDirs", err)
	checked := 0
	for key, m := range reg.All() {
		for _, ph := range m.PhaseDefs {
			if ph.ReviewLoop == nil {
				continue
			}
			checked++
			plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: ph.CoordinatorSurface}, 1)
			if err != nil {
				t.Errorf("%s phase %s: compile surface %q: %v", key, ph.ID, ph.CoordinatorSurface, err)
				continue
			}
			if !slices.Contains(plan.ImmediateNames(), "task") {
				t.Errorf("%s phase %s: surface %q does not offer task immediately", key, ph.ID, ph.CoordinatorSurface)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no shipped phase declares a review_loop")
	}
}
