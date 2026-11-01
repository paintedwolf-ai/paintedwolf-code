package runtime_test

import (
	"context"
	"testing"

	workflowruntime "github.com/lycaon/lycaon/internal/workflow/runtime"
)

type preparationRepairCapture struct{ calls int }

func (r *preparationRepairCapture) BlockContract(context.Context, string, error) error {
	r.calls++
	return nil
}

func TestCoordinatorProjectionDoesNotRecordReviewHold(t *testing.T) {
	mgr, _, blueprints, _ := testManagerWithRegistry(t)
	walkPlanRunToReview(t.Context(), t, mgr, blueprints, "sess-1")
	repairs := &preparationRepairCapture{}
	loader := &workflowruntime.CoordinatorFrames{Runs: mgr.Store.Runs, Resolver: &mgr.Resolver, Snapshots: mgr.Snapshots,
		Policy: mgr.Policy, Obligations: mgr.Obligations, Repairs: repairs,
		VerdictCatalog: func(context.Context, string) map[string]any { return map[string]any{"type": "object"} },
	}
	if _, err := loader.ProjectCoordinatorTurnFrame(t.Context(), "sess-1", nil); err == nil {
		t.Fatal("projection accepted an invalid review contract")
	}
	if repairs.calls != 0 {
		t.Fatal("projection recorded a review hold")
	}
	if _, err := loader.BuildCoordinatorTurnFrame(t.Context(), "sess-1", nil); err == nil {
		t.Fatal("normal turn accepted an invalid review contract")
	}
	if repairs.calls != 1 {
		t.Fatalf("normal turn recorded %d review holds, want 1", repairs.calls)
	}
}
