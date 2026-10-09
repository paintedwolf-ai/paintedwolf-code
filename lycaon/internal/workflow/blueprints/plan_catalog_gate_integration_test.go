//go:build integration

package blueprints_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
)

func TestResearchDepthNoneSetsResearchPhaseSkippedPredicate(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "completePlanResearchAtDepthNone", err)
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "mgr.Store.Runs.GetScaffoldVars failed", err)

	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	ok, err := reg.Evaluate("phase_skipped:research", conditions.EvalContext{Vars: vars})
	if err != nil || !ok {
		t.Fatalf("phase_skipped:research after depth=none = %v err=%v vars=%v", ok, err, vars)
	}
}
