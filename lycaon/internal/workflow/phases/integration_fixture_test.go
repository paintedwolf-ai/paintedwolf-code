package phases_test

import (
	"context"
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
	workflow "github.com/lycaon/lycaon/internal/workflow"
	workflowblueprintfiles "github.com/lycaon/lycaon/internal/workflow/blueprintfiles"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

// testRunStore wires the authorization recorder required for blueprint approvals.
func testRunStore(t *testing.T, sqlDB db.Handle) *runstate.Repository {
	t.Helper()
	s := workflowpersistence.New(sqlDB)
	s.Transactions.SetAuthzRecorder(authzcontext.SQLRecorder(sqlDB))
	return s
}

func testManager(t *testing.T) (*workflow.RunManager, session.Store, *blueprint.Manager, string) {
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
	mgr := workflow.NewManager(testRunStore(t, sqlDB), sessStore, manifestRegistry, nil)
	mgr.Blueprints.Scaffold.Store = workflowpersistence.NewSessionScaffoldSQLStore(sqlDB)
	blueprintMgr.AfterRetarget = mgr.Blueprints.RebindBlueprintPath
	mgr.Blueprints.Creator = blueprint.WorkflowBlueprintCreator{Manager: blueprintMgr}
	mgr.Blueprints.Getter = blueprintMgr
	mgr.Presentation.BlueprintGetter = blueprintMgr
	mgr.Approvals.Getter = blueprintMgr
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

func registryDepsForTests(mgr *workflow.RunManager, blueprintMgr *blueprint.Manager, base conditions.RegistryDeps) conditions.RegistryDeps {
	if blueprintMgr != nil {
		base.BlueprintGet = func(ctx context.Context, path string) (*api.Blueprint, error) {
			return blueprintMgr.Get(ctx, testdbseed.DefaultProjectID, path)
		}
		base.BlueprintContent = func(_ context.Context, projectDir, relPath string) (string, error) {
			return workflowblueprintfiles.ReadBlueprintFile(projectDir, relPath)
		}
	}
	if base.ChildRunStatus == nil && mgr != nil && mgr.Store != nil {
		store := mgr.Store
		base.ChildRunStatus = func(parentRunID string) (string, bool) {
			child, err := store.Runs.LatestChildByParentRunID(context.Background(), parentRunID)
			if err != nil || child == nil || !runstate.IsTerminal(child.Status) {
				return "", false
			}
			return string(child.Status), true
		}
	}
	return base
}

func setTestRegistry(t *testing.T, mgr *workflow.RunManager, blueprintMgr *blueprint.Manager, base conditions.RegistryDeps) {
	t.Helper()
	reg, err := conditions.NewDefaultRegistry(registryDepsForTests(mgr, blueprintMgr, base))
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	mgr.SetConditionRegistry(reg)
}

func testManagerWithRegistry(t *testing.T) (*workflow.RunManager, session.Store, *blueprint.Manager, string) {
	t.Helper()
	mgr, store, blueprintMgr, projectDir := testManager(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDeps())
	return mgr, store, blueprintMgr, projectDir
}

func workflowCaller(t testing.TB, mgr *workflow.RunManager) context.Context {
	t.Helper()
	owner, err := mgr.Policy.Sessions.(session.Store).HostOwner(context.Background())
	testutil.FailErr(t, "host owner", err)
	return people.WithCaller(context.Background(), owner)
}

func testWorkflowManager(t *testing.T) (*workflow.RunManager, string, string) {
	t.Helper()
	projectDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "wf.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)

	sessStore := store.NewSQL(sqlDB)
	sess, err := sessStore.Create(context.Background(), api.CreateSessionRequest{
		Posture:   api.SessionPostureBuild,
		ProjectID: testdbseed.DefaultProjectID,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "sessStore.Create failed", err)
	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs failed", err)
	blueprintStore := blueprint.NewFileStoreForTest(projectDir)
	blueprintMgr := blueprint.NewManager(blueprintStore)
	runStore := workflowpersistence.New(sqlDB)
	mgr := workflow.NewManager(runStore, sessStore, manifestRegistry, nil)
	mgr.Blueprints.Scaffold.Store = workflowpersistence.NewSessionScaffoldSQLStore(sqlDB)
	blueprintMgr.AfterRetarget = mgr.Blueprints.RebindBlueprintPath
	mgr.Blueprints.Creator = blueprint.WorkflowBlueprintCreator{Manager: blueprintMgr}
	mgr.Blueprints.Getter = blueprintMgr
	mgr.Presentation.BlueprintGetter = blueprintMgr
	mgr.Approvals.Getter = blueprintMgr
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "conditions.NewDefaultRegistry", err)
	mgr.SetConditionRegistry(reg)
	return mgr, sess.ID, projectDir
}
