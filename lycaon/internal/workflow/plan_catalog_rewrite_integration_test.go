//go:build integration

package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPlanCatalogHappyPathReachesExecutionSubroutine(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDepsWithEvidence())
	ctx := context.Background()

	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "complete research at depth none", err)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	run, err = advancePlanThroughExpand(ctx, mgr, run)
	testutil.FailErr(t, "advance expand", err)
	run, err = completePlanReviewAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "skip review", err)
	run, err = mgr.SyncHumanApproval(workflowCaller(t, mgr), run.ID, projectDir)
	testutil.FailErr(t, "SyncHumanApproval", err)
	if run.CurrentPhase != "execute" {
		t.Fatalf("phase = %q want execute after approval", run.CurrentPhase)
	}
	if run.Status != api.WorkflowRunStatusPausedOnChild {
		t.Fatalf("status = %q want paused_on_child", run.Status)
	}
	child, err := mgr.Store.ActiveBySession(ctx, "sess-1")
	testutil.FailErr(t, "ActiveBySession", err)
	if child == nil || child.WorkflowID != "implement" {
		t.Fatalf("child = %+v want implement leaf", child)
	}
}

func TestPlanResearchDepthNoneReachesExecutionSubroutine(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "plan-execute.db")

	sessStore := store.NewSQL(sqlDB)
	dir := t.TempDir()
	blueprintStore := blueprint.NewFileStoreForTest(dir)
	blueprintMgr := blueprint.NewManager(blueprintStore)
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	mgr := NewManager(testRunStore(t, sqlDB), sessStore, reg, nil)
	mgr.Resolver = ManifestResolver{}
	WireBlueprintDepsForTest(mgr, dir)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDepsWithEvidence())

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create session", err)

	parent, err := mgr.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{WorkflowID: "plan", WorkflowVersion: "1.0.0", Request: "test request"})
	testutil.FailErr(t, "Start plan", err)
	parent = completePlanIntakeT(ctx, t, mgr, parent)
	parent, err = completePlanResearchAtDepthNone(ctx, mgr, parent)
	testutil.FailErr(t, "complete research at depth none", err)
	if parent.CurrentPhase != "expand" {
		t.Fatalf("phase after research depth none = %q want expand", parent.CurrentPhase)
	}
	seedValidPlanContent(t, blueprintMgr, parent.BlueprintPath)
	parent, err = mgr.TryAutoAdvanceThroughCommittedGates(ctx, parent.ID, 8)
	testutil.FailErr(t, "auto-advance plan to approve", err)
	if parent.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve before human approval", parent.CurrentPhase)
	}
	parent, err = mgr.SyncHumanApproval(workflowCaller(t, mgr), parent.ID, dir)
	testutil.FailErr(t, "SyncHumanApproval", err)
	if parent.CurrentPhase != "execute" {
		t.Fatalf("phase = %q want execute", parent.CurrentPhase)
	}
	if parent.Status != api.WorkflowRunStatusPausedOnChild {
		t.Fatalf("status = %q want paused_on_child", parent.Status)
	}
}

func TestPlanManifestHasNoImplementWorkflowReadyOnExecute(t *testing.T) {
	raw, _, err := workflowdef.LoadPackManifestsForCatalog(nil)
	testutil.FailErr(t, "loadCatalogManifestsWithSources", err)
	planManifest, ok := raw[workflowdef.ManifestKey("plan", "1.0.0")]
	if !ok {
		t.Fatal("missing plan manifest")
	}
	for _, p := range planManifest.PhaseDefs {
		if p.ID != "build" {
			continue
		}
		if p.EntryWhen == "implement_workflow_ready" {
			t.Fatal("build phase must not use implement_workflow_ready entry_when")
		}
	}
}
