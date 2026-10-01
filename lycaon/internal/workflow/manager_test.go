package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// testRunStore wires the authorization recorder required for blueprint approvals.
func testRunStore(t *testing.T, sqlDB db.Handle) *SQLStore {
	t.Helper()
	s := NewSQLStore(sqlDB)
	s.SetAuthzRecorder(authzcontext.SQLRecorder(sqlDB))
	return s
}

func testManager(t *testing.T) (*RunManager, session.Store, *blueprint.Manager, string) {
	t.Helper()
	projectDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "store.db")

	testdbseed.InsertSessionWithRoot(t, sqlDB, "sess-1", testdbseed.DefaultProjectID, projectDir)

	sessStore := store.NewSQL(sqlDB)
	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs failed", err)
	blueprintStore := blueprint.NewFileStoreForTest(projectDir)
	blueprintMgr := blueprint.NewManager(blueprintStore)
	blueprintMgr.Approvals = blueprint.NewApprovalStore(sqlDB, authzcontext.SQLRecorder(sqlDB))
	mgr := NewManager(testRunStore(t, sqlDB), sessStore, manifestRegistry, nil)
	mgr.SessionScaffold = NewSessionScaffoldSQLStore(sqlDB)
	blueprintMgr.AfterRetarget = mgr.RebindBlueprintPath
	mgr.BlueprintCreate = blueprint.WorkflowBlueprintCreator{Manager: blueprintMgr}
	mgr.BlueprintGet = blueprintMgr
	return mgr, sessStore, blueprintMgr, projectDir
}

func seedValidPlanContent(t *testing.T, blueprintMgr *blueprint.Manager, blueprintPath string) {
	t.Helper()
	if blueprintMgr == nil || blueprintPath == "" {
		t.Fatal("blueprint manager and blueprint path required")
	}
	ctx := context.Background()
	bp, err := blueprintMgr.Get(ctx, testdbseed.DefaultProjectID, blueprintPath)
	testutil.FailErr(t, "blueprintMgr.Get", err)
	content := conditions.TestPlanContentWithTasks
	if _, err := blueprintMgr.Store.UpdateContent(ctx, testdbseed.DefaultProjectID, blueprintPath, content, blueprint.ContentDigest(bp.Content)); err != nil {
		testutil.FailErr(t, "seed blueprint content", err)
	}
}

func registryDepsForTests(mgr *RunManager, blueprintMgr *blueprint.Manager, base conditions.RegistryDeps) conditions.RegistryDeps {
	if blueprintMgr != nil {
		base.BlueprintGet = func(ctx context.Context, path string) (*api.Blueprint, error) {
			return blueprintMgr.Get(ctx, testdbseed.DefaultProjectID, path)
		}
		base.BlueprintContent = func(_ context.Context, projectDir, relPath string) (string, error) {
			return ReadBlueprintFile(projectDir, relPath)
		}
	}
	if base.ChildRunStatus == nil && mgr != nil && mgr.Store != nil {
		store := mgr.Store
		base.ChildRunStatus = func(parentRunID string) (string, bool) {
			child, err := store.LatestChildByParentRunID(context.Background(), parentRunID)
			if err != nil || child == nil || !IsTerminal(child.Status) {
				return "", false
			}
			return string(child.Status), true
		}
	}
	return base
}

func setTestRegistry(t *testing.T, mgr *RunManager, blueprintMgr *blueprint.Manager, base conditions.RegistryDeps) {
	t.Helper()
	reg, err := conditions.NewDefaultRegistry(registryDepsForTests(mgr, blueprintMgr, base))
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	mgr.SetConditionRegistry(reg)
}

func testManagerWithRegistry(t *testing.T) (*RunManager, session.Store, *blueprint.Manager, string) {
	t.Helper()
	mgr, store, blueprintMgr, projectDir := testManager(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDeps())
	return mgr, store, blueprintMgr, projectDir
}

func workflowCaller(t testing.TB, mgr *RunManager) context.Context {
	t.Helper()
	owner, err := mgr.Sessions.HostOwner(context.Background())
	testutil.FailErr(t, "host owner", err)
	return people.WithCaller(context.Background(), owner)
}

func TestRunManagerLifecycle(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	if run.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("status = %q", run.Status)
	}
	if run.CurrentPhase != "research" {
		t.Fatalf("phase = %q want research", run.CurrentPhase)
	}

	run, err = mgr.Pause(ctx, run.ID, "hold")
	testutil.FailErr(t, "mgr.Pause failed", err)
	if run.Status != api.WorkflowRunStatusPaused {
		t.Fatalf("status = %q", run.Status)
	}

	run, err = mgr.Pause(ctx, run.ID, "again")
	testutil.FailErr(t, "mgr.Pause failed", err)
	if run.Status != api.WorkflowRunStatusPaused {
		t.Fatalf("idempotent pause status = %q", run.Status)
	}

	if err := mgr.AssertRunnable(ctx, run.ID); err == nil {
		t.Fatal("expected paused run to be not runnable")
	}

	run, err = mgr.Resume(ctx, run.ID)
	testutil.FailErr(t, "mgr.Resume failed", err)
	if run.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("status = %q", run.Status)
	}

	run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "completePlanResearchAtDepthNone", err)
	if run.CurrentPhase != "expand" {
		t.Fatalf("phase after research_depth=none = %q want expand", run.CurrentPhase)
	}

	run, err = mgr.Cancel(ctx, run.ID, "done")
	testutil.FailErr(t, "mgr.Cancel failed", err)
	if run.Status != api.WorkflowRunStatusCanceled {
		t.Fatalf("status = %q", run.Status)
	}
	// The response timestamp matches the committed run.
	if run.UpdatedAt.IsZero() {
		t.Fatal("Cancel response: updated_at is zero — store assignment did not propagate to caller")
	}
	if run.CompletedAt != nil && run.UpdatedAt.Before(*run.CompletedAt) {
		t.Fatalf("updated_at %s precedes completed_at %s — caller saw stale value", run.UpdatedAt, *run.CompletedAt)
	}
	if _, ok := mgr.runVarsGuards.Load(run.ID); ok {
		t.Fatal("runVarsGuards entry survived a committed cancellation")
	}

	// A second cancel call returns the existing canceled run unchanged.
	prevUpdated := run.UpdatedAt
	again, err := mgr.Cancel(ctx, run.ID, "done")
	testutil.FailErr(t, "mgr.Cancel failed", err)
	if !again.UpdatedAt.Equal(prevUpdated) {
		t.Fatalf("idempotent cancel bumped updated_at: was %s now %s", prevUpdated, again.UpdatedAt)
	}
}

func TestForgetSessionReleasesStartAndAskUserGuards(t *testing.T) {
	m := &RunManager{}
	m.startGuardFor("sess-1")
	m.askUserGuardFor("sess-1")
	if _, ok := m.startGuards.Load("sess-1"); !ok {
		t.Fatal("expected a start guard to exist before ForgetSession")
	}
	if _, ok := m.askUserGuards.Load("sess-1"); !ok {
		t.Fatal("expected an ask-user guard to exist before ForgetSession")
	}
	m.ForgetSession("sess-1")
	if _, ok := m.startGuards.Load("sess-1"); ok {
		t.Fatal("startGuards entry survived ForgetSession")
	}
	if _, ok := m.askUserGuards.Load("sess-1"); ok {
		t.Fatal("askUserGuards entry survived ForgetSession")
	}
}

func TestStartRejectsDuplicateActiveRun(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	if _, err := mgr.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	}); err != nil {
		testutil.FailErr(t, "StartHuman", err)
	}
	if _, err := mgr.StartAmbient(ctx, sessionID, "plan", "1.0.0"); !errors.Is(err, ErrActiveRunExists) {
		t.Fatalf("err = %v", err)
	}
}

func TestAssertSessionRunnable(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	if err := mgr.AssertSessionRunnable(ctx, "sess-1"); err != nil {
		t.Fatalf("running: %v", err)
	}
	if _, err := mgr.Pause(ctx, run.ID, "test"); err != nil {
		testutil.FailErr(t, "mgr.Pause failed", err)
	}
	if err := mgr.AssertSessionRunnable(ctx, "sess-1"); err == nil {
		t.Fatal("expected paused session to be blocked")
	}
	if phase := mgr.CurrentPhase(ctx, "sess-1"); phase != "research" {
		t.Fatalf("phase = %q", phase)
	}
}

func TestAdvanceWhilePaused(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	if _, err := mgr.Pause(ctx, run.ID, "review"); err != nil {
		testutil.FailErr(t, "mgr.Pause failed", err)
	}
	run, err = mgr.Advance(ctx, run.ID)
	testutil.FailErr(t, "mgr.Advance failed", err)
	if run.CurrentPhase != "expand" {
		t.Fatalf("phase = %q", run.CurrentPhase)
	}
	if run.Status != api.WorkflowRunStatusPaused {
		t.Fatalf("advance should not resume: status = %q", run.Status)
	}
}

func TestCompleteOnFinalAdvance(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDepsWithEvidence())
	ctx := workflowCaller(t, mgr)
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	run, err = advancePlanToApprovePhase(ctx, mgr, run)
	testutil.FailErr(t, "advancePlanToApprovePhase", err)
	run, err = mgr.SyncHumanApproval(ctx, run.ID, projectDir)
	testutil.FailErr(t, "SyncHumanApproval", err)
	if run.CurrentPhase != "execute" {
		t.Fatalf("phase = %q want execute", run.CurrentPhase)
	}
	child, err := mgr.Store.ActiveBySession(ctx, "sess-1")
	testutil.FailErr(t, "ActiveBySession", err)
	if child == nil {
		t.Fatal("expected implement child")
	}
	TerminalChildRunForTest(ctx, t, mgr, child.ID)
	run, err = mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "Get parent", err)
	if run.Status != api.WorkflowRunStatusComplete {
		t.Fatalf("status = %q", run.Status)
	}
	if run.CompletedAt == nil {
		t.Fatal("expected completed_at")
	}
}
