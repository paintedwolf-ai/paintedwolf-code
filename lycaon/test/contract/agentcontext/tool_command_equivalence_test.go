package contract

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/toolcontract"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestRoutineFloorsOfferCommandInPlaceOfRetiredSchemas(t *testing.T) {
	t.Parallel()
	retiredOperations, err := toolcontract.CommandOperations()
	contractcheck.FailErr(t, "load command operations", err)
	catalog, err := surfacecatalog.Load()
	contractcheck.FailErr(t, "load coordinator surfaces", err)
	for _, id := range catalog.SurfaceIDs() {
		surface, err := catalog.Surface(id)
		contractcheck.FailErr(t, "read surface "+id, err)
		floor := surface.FloorTools()
		for _, retired := range retiredOperations {
			if slices.Contains(floor, retired) || slices.Contains(surface.LoadableTools(), retired) {
				t.Errorf("%s still offers command operation %s as a tool", id, retired)
			}
		}
		if slices.Contains(floor, "command") && slices.Contains(surface.LoadableTools(), "command") {
			t.Errorf("%s lists command on both its floor and its loadable set", id)
		}
	}
	for _, id := range []string{"implement_investigate", "observe_investigate", "plan_review", "await_host"} {
		surface, err := catalog.Surface(id)
		if err != nil || !slices.Contains(surface.FloorTools(), "command") {
			t.Errorf("%s does not offer command on its floor", id)
		}
	}
}

func TestToolProfilesOmitCommandOperationsAndOfferReadOnlyCommand(t *testing.T) {
	t.Parallel()
	retiredOperations, err := toolcontract.CommandOperations()
	contractcheck.FailErr(t, "load command operations", err)
	for _, id := range []string{"coordinator", "implement", "worker_readonly", "explore_readonly", "plan_review_readonly"} {
		profile := loadToolProfileByID(t, id)
		for _, retired := range retiredOperations {
			if profile.ToolAllowed(retired) || profile.StickyTools[retired] {
				t.Errorf("%s offers command operation %s as a tool", id, retired)
			}
		}
		if !profile.ToolAllowed("command") || !profile.StickyTools["command"] {
			t.Errorf("%s does not offer command up front", id)
		}
	}
	for _, id := range []string{"worker_readonly", "explore_readonly", "plan_review_readonly"} {
		if classes := loadToolProfileByID(t, id).CommandEffects.Classes(); !slices.Equal(classes, []toolcontract.CommandEffectClass{toolcontract.CommandReadOnly}) {
			t.Errorf("%s command is not read-only", id)
		}
	}
}
