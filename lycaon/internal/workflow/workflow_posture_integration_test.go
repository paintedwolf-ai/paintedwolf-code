//go:build integration

package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkflowModeIntegrationImplementOnEnter(t *testing.T) {
	mgr, sessStore, blueprintMgr, projectDir := testManagerWithRegistry(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDeps())
	ctx := context.Background()

	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	sess, err := sessStore.Get(ctx, "sess-1")
	testutil.FailErr(t, "sessStore.Get failed", err)
	if sess.Posture != api.SessionPostureSpec {
		t.Fatalf("start posture = %q want spec", sess.Posture)
	}

	run, err = advancePlanToApprovePhase(ctx, mgr, run)
	testutil.FailErr(t, "advancePlanToApprovePhase", err)
	run, err = mgr.Approvals.SyncHumanApproval(workflowCaller(t, mgr), run.ID, projectDir)
	testutil.FailErr(t, "SyncHumanApproval", err)
	if run.CurrentPhase != "execute" {
		t.Fatalf("phase = %q want execute", run.CurrentPhase)
	}
	sess, err = sessStore.Get(ctx, "sess-1")
	testutil.FailErr(t, "sessStore.Get failed", err)
	if sess.Posture != api.SessionPostureBuild {
		t.Fatalf("implement posture = %q", sess.Posture)
	}
}

func TestAdvanceBlockedWhenGatesUnmet(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "gate.db")
	sessStore := store.NewSQL(sqlDB)
	testdbseed.InsertProject(t, sqlDB, testdbseed.DefaultProjectID)
	testdbseed.InsertDraftScratchRoot(t, sqlDB, testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, sqlDB, "sess-g", testdbseed.DefaultProjectID)
	reg := workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		workflowdef.ManifestKey("gated", "1.0.0"): workflowdef.FinalizeManifest(workflowdef.Manifest{
			ID:      "gated",
			Version: "1.0.0",
			PhaseDefs: []workflowdef.PhaseDef{
				{ID: "verify", CompleteWhen: "gates_satisfied", Gates: []string{"evidence_passed:verify"}},
			},
		}),
	})
	mgr := NewManager(workflowpersistence.New(sqlDB), sessStore, reg, nil)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-g", "gated", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	if !mgr.Policy.ActivePhaseRequiresEvidence(ctx, "sess-g", "verify") {
		t.Fatal("declared verify gate must opt the phase into verification")
	}
	if mgr.Policy.ActivePhaseRequiresEvidence(ctx, "sess-g", "security") {
		t.Fatal("undeclared evidence type must remain optional")
	}
	if _, err := mgr.Phases.Advance(ctx, run.ID); err == nil {
		t.Fatal("expected phase gate unmet")
	} else if _, ok := runstate.IsPhaseGateUnmet(err); !ok {
		t.Fatalf("err = %v", err)
	}
	run, err = mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get after blocked advance", err)
	vars := runstate.SetGateSatisfied(map[string]any{}, "evidence_passed:verify", true)
	if err := mgr.Store.State.UpdateVars(ctx, run, "/tmp/p", vars); err != nil {
		testutil.FailErr(t, "mgr.Store.State.UpdateVars failed", err)
	}
	if _, err := mgr.Phases.Advance(ctx, run.ID); err != nil {
		t.Fatalf("advance after gate satisfied: %v", err)
	}
}

func TestPlanAutoApproveParamsEffectivePhases(t *testing.T) {
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs failed", err)
	plan, err := reg.Get("plan", "1.0.0")
	testutil.FailErr(t, "Get plan", err)
	vars := runstate.ApplyMergedParams(nil, map[string]string{
		"research_depth": "none",
		"auto_approve":   "true",
	})
	vars = runstate.StampDepthParamSkips(vars, plan)
	if !conditions.DotPathTruthy(vars, "phase_skipped.research") {
		t.Fatal("research_depth=none should stamp phase_skipped.research")
	}
	if conditions.DotPathTruthy(vars, "phase_skipped.review") {
		t.Fatal("research_depth=none alone must not skip transition-only review")
	}
}
