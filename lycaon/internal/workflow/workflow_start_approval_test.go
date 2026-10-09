package workflow

import (
	"context"
	"errors"
	workflowblueprintfiles "github.com/lycaon/lycaon/internal/workflow/blueprintfiles"
	"testing"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/blueprintfile"
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

func testWorkflowManager(t *testing.T) (*RunManager, string, string) {
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
	mgr := NewManager(runStore, sessStore, manifestRegistry, nil)
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

func TestStateStartRequiresHumanApproval(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	_, err := mgr.Starts.Start(context.Background(), sessionID, api.StartWorkflowRunRequest{
		WorkflowID:      "plan",
		WorkflowVersion: "1.0.0",
	})
	if !errors.Is(err, runstate.ErrWorkflowStartRequiresHumanApproval) {
		t.Fatalf("Start err = %v want WORKFLOW_START_REQUIRES_HUMAN_APPROVAL", err)
	}
}

func TestStateStartAfterSlashNoExtraApproval(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	_, handled, err := mgr.Slash.TrySlashPrompt(context.Background(), sessionID, "/plan", "")
	testutil.FailErr(t, "mgr.Slash.TrySlashPrompt failed", err)
	if !handled {
		t.Fatal("expected /plan slash handled")
	}
	active, err := mgr.Store.Runs.ActiveBySession(context.Background(), sessionID)
	testutil.FailErr(t, "mgr.GetActive failed", err)
	if active.BlueprintPath == "" {
		t.Fatal("expected blueprint_path on slash-started run")
	}
}

func TestSlashStartSupersedesReviewedCatalogRun(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	first, err := mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID:      "plan",
		WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "mgr.StartHuman", err)

	_, handled, err := mgr.Slash.TrySlashPrompt(ctx, sessionID, "/options", "")
	testutil.FailErr(t, "mgr.Slash.TrySlashPrompt", err)
	if !handled {
		t.Fatal("expected /options slash handled")
	}
	prior, err := mgr.Store.Runs.Get(ctx, first.ID)
	testutil.FailErr(t, "mgr.Store.Runs.Get first", err)
	if prior.Status != api.WorkflowRunStatusCanceled {
		t.Fatalf("first status = %q want canceled", prior.Status)
	}
	active, err := mgr.Store.Runs.ActiveBySession(ctx, sessionID)
	testutil.FailErr(t, "mgr.GetActive", err)
	if active == nil || active.WorkflowID != "options" {
		t.Fatalf("active = %+v, want options", active)
	}
}

func TestSlashStartBlueprintTitleUsesCurrentIntent(t *testing.T) {
	mgr, sessionID, projectDir := testWorkflowManager(t)
	ctx := context.Background()
	_, handled, err := mgr.Slash.TrySlashPrompt(ctx, sessionID, "/options Choose package or CLI", "")
	testutil.FailErr(t, "mgr.Slash.TrySlashPrompt", err)
	if !handled {
		t.Fatal("expected /options slash handled")
	}
	active, err := mgr.Store.Runs.ActiveBySession(ctx, sessionID)
	testutil.FailErr(t, "mgr.GetActive", err)
	content, err := workflowblueprintfiles.ReadBlueprintFile(projectDir, active.BlueprintPath)
	testutil.FailErr(t, "workflowblueprintfiles.ReadBlueprintFile", err)
	title, ok := blueprintfile.DeclaredTitle(content)
	if !ok || title != blueprint.SlugTitle("Choose package or CLI") {
		t.Fatalf("Blueprint title = %q ok=%v", title, ok)
	}
}

func TestHumanStartAfterProposal(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	_, err := mgr.Ambient.StartAmbient(context.Background(), sessionID, "implement", "1.0.0")
	testutil.FailErr(t, "mgr.Ambient.StartAmbient failed", err)
	if err := mgr.Blueprints.Scaffold.NoteWorkflowStartProposal(context.Background(), sessionID, "plan", "1.0.0"); err != nil {
		testutil.FailErr(t, "mgr.Blueprints.Scaffold.NoteWorkflowStartProposal failed", err)
	}
	run, err := mgr.Starts.StartHuman(context.Background(), sessionID, api.StartWorkflowRunRequest{
		WorkflowID:      "plan",
		WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "mgr.StartHuman failed", err)
	if run.BlueprintPath == "" {
		t.Fatal("expected blueprint_path on human start")
	}
}

func TestStateStartChatDoesNotGrantApproval(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	if err := mgr.Blueprints.Scaffold.NoteWorkflowStartProposal(context.Background(), sessionID, "plan", "1.0.0"); err != nil {
		testutil.FailErr(t, "mgr.Blueprints.Scaffold.NoteWorkflowStartProposal failed", err)
	}
	for _, msg := range []string{"yes, start plan", "go ahead", "lgtm", "approved"} {
		testutil.FailErr(t, "TryResolveUserFeedback", mgr.Feedback.TryResolveUserFeedback(context.Background(), sessionID, "", testutil.HostOwner().ID, msg))
	}
	_, err := mgr.Starts.Start(context.Background(), sessionID, api.StartWorkflowRunRequest{
		WorkflowID:      "plan",
		WorkflowVersion: "1.0.0",
	})
	if !errors.Is(err, runstate.ErrWorkflowStartRequiresHumanApproval) {
		t.Fatalf("Start err = %v want WORKFLOW_START_REQUIRES_HUMAN_APPROVAL after chat affirmation", err)
	}
}

func TestStateStartRejectsAmbientAttachWorkflow(t *testing.T) {
	reg := workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		workflowdef.ManifestKey("implement", "1.0.0"): {
			ID: "implement", Version: "1.0.0",
			Attach: workflowdef.ManifestAttach{Policy: workflowdef.AttachPolicySessionCreate},
		},
	})
	if reg.CatalogStartable("implement", "1.0.0") {
		t.Fatal("session_create implement must not be catalog-startable")
	}
}
