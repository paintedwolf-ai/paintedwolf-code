package presentation_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

// Approval chrome is general; blueprint chrome follows the run's blueprint. Both
// are read from the run, so a second recipe that declares a blueprint gets the
// same treatment as the first.
func TestComputeRunUIChromeFollowsApprovalPhaseAndBlueprint(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	run, err := mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman options", err)
	// Awaiting requires the run to sit on a human_approval phase (select), not
	// just the vars — chrome off that phase is a dead Approve button.
	run.CurrentPhase = "select"
	testutil.FailErr(t, "Store.Update to select", mgr.Store.State.Update(ctx, run))
	if err := mgr.Store.State.UpdateVars(ctx, run, "/tmp/p", map[string]any{
		"human_approval": map[string]any{"active": true, "ready": true, "blueprint_path": run.BlueprintPath},
	}); err != nil {
		testutil.FailErr(t, "UpdateVars", err)
	}
	ui, err := mgr.Presentation.ComputeRunUI(ctx, run)
	testutil.FailErr(t, "ComputeRunUI", err)
	if ui == nil || !ui.HumanApprovalAwaiting {
		t.Fatal("expected human_approval_awaiting off the select phase")
	}
	// options declares a blueprint, so its revision is chrome it should carry.
	if ui.PlanRevisionAt == nil {
		t.Fatal("expected plan_revision_at for a run that carries a blueprint")
	}

	run.BlueprintPath = ""
	ui, err = mgr.Presentation.ComputeRunUI(ctx, run)
	testutil.FailErr(t, "ComputeRunUI without a blueprint", err)
	if ui == nil || ui.HumanApprovalAwaiting {
		t.Fatal("approval chrome must fail closed without its blueprint")
	}
	if ui.PlanRevisionAt != nil {
		t.Fatal("blueprint chrome must stay unset for a run with no blueprint")
	}
}
