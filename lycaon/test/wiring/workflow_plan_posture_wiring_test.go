package wiring

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/blueprint/blueprinttest"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowblueprintfiles "github.com/lycaon/lycaon/internal/workflow/blueprintfiles"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	workflowruntime "github.com/lycaon/lycaon/internal/workflow/runtime"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPlanWorkflowPostureLifecycleContract(t *testing.T) {
	ctx, mgr, sessStore, blueprintMgr := newBundledPlanWorkflowManager(t)
	run, bp := startSeededPlanRun(t, ctx, mgr, blueprintMgr)
	assertSessionPosture(t, ctx, sessStore, "sess-posture", api.SessionPostureSpec)

	run, err := mgr.Phases.Advance(ctx, run.ID)
	testutil.FailErr(t, "advance expand", err)
	if run.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve", run.CurrentPhase)
	}
	assertSessionPosture(t, ctx, sessStore, "sess-posture", api.SessionPostureSpec)

	run = approveSeededPlan(t, ctx, mgr, sessStore, run, bp)
	if run.CurrentPhase != "execute" {
		t.Fatalf("phase = %q want execute", run.CurrentPhase)
	}
	assertSessionPosture(t, ctx, sessStore, "sess-posture", api.SessionPostureBuild)
}

// Approving the bundled plan enters execute, which runs implement as a child run.
func TestBundledPlanExecutionSubroutine(t *testing.T) {
	ctx, mgr, sessStore, blueprintMgr := newBundledPlanWorkflowManager(t)
	run, bp := startSeededPlanRun(t, ctx, mgr, blueprintMgr)
	run, err := mgr.Phases.Advance(ctx, run.ID)
	testutil.FailErr(t, "advance expand", err)
	parent := approveSeededPlan(t, ctx, mgr, sessStore, run, bp)
	child, err := mgr.Store.Runs.LatestChildByParentRunID(ctx, parent.ID)
	testutil.FailErr(t, "load execute child run", err)
	if child == nil || child.WorkflowID != "implement" || child.ParentRunID == nil || *child.ParentRunID != parent.ID {
		t.Fatalf("execute child run = %+v want implement under %s", child, parent.ID)
	}
}

// An unsatisfied research gate reaches the inject and the turn's failed leaves,
// and leaves both once the var is set.
func TestPlanResearchObligationsInInject(t *testing.T) {
	ctx, wfMgr, _, blueprintMgr := newBundledPlanWorkflowManager(t)
	frameLoader := &workflowruntime.CoordinatorFrames{Runs: wfMgr.Store.Runs, Resolver: &wfMgr.Resolver, Snapshots: wfMgr.Snapshots, Policy: wfMgr.Policy, Obligations: wfMgr.Obligations}

	run, err := wfMgr.Starts.StartHuman(ctx, "sess-posture", api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.1", Request: "test request",
	})
	testutil.FailErr(t, "StartHuman plan", err)
	if run.CurrentPhase != "research" {
		t.Fatalf("phase = %q want research", run.CurrentPhase)
	}
	seed, err := blueprintMgr.Get(ctx, run.ProjectID, run.BlueprintPath)
	testutil.FailErr(t, "blueprintMgr.Get", err)
	researchPlan := strings.Replace(conditions.TestPlanContentWithTasks, "research_depth: none", "research_depth: light", 1)
	_, err = blueprintMgr.Store.UpdateContent(ctx, run.ProjectID, run.BlueprintPath, researchPlan, blueprint.ContentDigest(seed.Content))
	testutil.FailErr(t, "seed research plan", err)
	if _, err := wfMgr.Phases.Advance(ctx, run.ID); err == nil {
		t.Fatal("expected research gate to block advance")
	}

	frame, block := renderWorkflowFrame(t, ctx, frameLoader, "sess-posture", nil)
	if !slices.Contains(frame.RunContext.FailedLeaves, "research_satisfied") || !strings.Contains(block, "research_satisfied") {
		t.Fatalf("failed leaves = %v; inject:\n%s", frame.RunContext.FailedLeaves, block)
	}

	vars, err := wfMgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars["research_satisfied"] = true
	run, err = wfMgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "reload run after rejected advance", err)
	run.CurrentPhase = "research"
	testutil.FailErr(t, "CommitState", wfMgr.Store.State.CommitState(ctx, run, "", vars))

	frame, _ = renderWorkflowFrame(t, ctx, frameLoader, "sess-posture", nil)
	if slices.Contains(frame.RunContext.FailedLeaves, "research_satisfied") {
		t.Fatalf("satisfied gate still failing: %v", frame.RunContext.FailedLeaves)
	}
}

func startSeededPlanRun(t *testing.T, ctx context.Context, mgr *workflow.RunManager, blueprintMgr *blueprint.Manager) (*api.WorkflowRun, *api.Blueprint) {
	t.Helper()
	run, err := mgr.Starts.StartHuman(ctx, "sess-posture", api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.1",
		Request:    "test request",
		Parameters: map[string]string{"research_depth": "none"},
	})
	testutil.FailErr(t, "mgr.StartHuman failed", err)
	if run.CurrentPhase != "expand" {
		t.Fatalf("depth-none start phase = %q want expand", run.CurrentPhase)
	}
	seed, err := blueprintMgr.Get(ctx, run.ProjectID, run.BlueprintPath)
	testutil.FailErr(t, "blueprintMgr.Get before seed", err)
	bp, err := blueprintMgr.Store.UpdateContent(ctx, run.ProjectID, run.BlueprintPath, conditions.TestPlanContentWithTasks, blueprint.ContentDigest(seed.Content))
	testutil.FailErr(t, "seed blueprint content", err)
	return run, bp
}

func approveSeededPlan(t *testing.T, ctx context.Context, mgr *workflow.RunManager, sessStore session.Store, run *api.WorkflowRun, bp *api.Blueprint) *api.WorkflowRun {
	t.Helper()
	sess, err := sessStore.Get(ctx, "sess-posture")
	testutil.FailErr(t, "store.Get failed", err)
	run, err = mgr.Approvals.ApprovePlan(
		ctx, run.ProjectID, run.BlueprintPath, sess.WorkspacePath,
		run.ID, run.Revision, workflowdef.HashBlueprintContent(bp.Content),
	)
	testutil.FailErr(t, "ApprovePlan", err)
	return run
}

// newBundledPlanWorkflowManager builds the plan workflow stack over a
// disposable store and returns the host owner's caller context, since plan
// approval is an authenticated decision.
func newBundledPlanWorkflowManager(t *testing.T) (context.Context, *workflow.RunManager, session.Store, *blueprint.Manager) {
	t.Helper()
	projectDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "posture-lifecycle.db")
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)

	testdbseed.InsertSessionWithRoot(t, sqlDB, "sess-posture", testdbseed.DefaultProjectID, projectDir)

	sessStore := store.NewSQL(sqlDB)
	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "workflow.RegistryFromDirs failed", err)
	blueprintStore := blueprinttest.NewFileStore(projectDir)
	blueprintMgr := blueprint.NewManager(blueprintStore)
	runStore := workflowpersistence.New(sqlDB)
	// The seal is a precondition of the grant, so the run store carries the same
	// recorder the sidecar wires.
	runStore.Transactions.SetAuthzRecorder(authzcontext.SQLRecorder(sqlDB))
	mgr := workflow.NewManager(runStore, sessStore, manifestRegistry, nil)
	blueprintMgr.AfterRetarget = mgr.Blueprints.RebindBlueprintPath
	mgr.Blueprints.Creator = blueprint.WorkflowBlueprintCreator{Manager: blueprintMgr}
	mgr.Blueprints.Getter = blueprintMgr
	mgr.Presentation.BlueprintGetter = blueprintMgr
	mgr.Approvals.Getter = blueprintMgr
	condReg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		BlueprintGet: func(ctx context.Context, path string) (*api.Blueprint, error) {
			return blueprintMgr.Get(ctx, testdbseed.DefaultProjectID, path)
		},
		BlueprintContent: func(_ context.Context, projectDir, relPath string) (string, error) {
			return workflowblueprintfiles.ReadBlueprintFile(projectDir, relPath)
		},
	})
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	mgr.SetConditionRegistry(condReg)
	return ctx, mgr, sessStore, blueprintMgr
}

func assertSessionPosture(t *testing.T, ctx context.Context, store session.Store, id string, want api.SessionPosture) {
	t.Helper()
	sess, err := store.Get(ctx, id)
	testutil.FailErr(t, "store.Get failed", err)
	if sess.Posture != want {
		t.Fatalf("session %s posture = %q want %q", id, sess.Posture, want)
	}
}

// renderWorkflowFrame builds the coordinator turn frame and renders its active
// workflow inject with the bundled hint and gate feedback catalogs.
func renderWorkflowFrame(t *testing.T, ctx context.Context, loader *workflowruntime.CoordinatorFrames, sessionID string, codes []string) (inject.CoordinatorTurnFrame, string) {
	t.Helper()
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfigStock", err)
	gateFeedback, err := feedback.LoadGateFeedbackCatalog()
	testutil.FailErr(t, "LoadGateFeedbackCatalog", err)
	frame, err := loader.BuildCoordinatorTurnFrame(ctx, sessionID, nil)
	testutil.FailErr(t, "BuildCoordinatorTurnFrame", err)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()})
	block, err := inject.RenderActiveWorkflowInject(ctx, prompts.NewInjectRenderer(engine), "sess-inject-test", frame, hints, codes, gateFeedback)
	testutil.FailErr(t, "RenderActiveWorkflowInject", err)
	return frame, block
}
