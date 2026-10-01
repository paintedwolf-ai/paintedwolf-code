package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/blueprintfile"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
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
	runStore := NewSQLStore(sqlDB)
	mgr := NewManager(runStore, sessStore, manifestRegistry, nil)
	mgr.Resolver = ManifestResolver{}
	mgr.SessionScaffold = NewSessionScaffoldSQLStore(sqlDB)
	blueprintMgr.AfterRetarget = mgr.RebindBlueprintPath
	mgr.BlueprintCreate = blueprint.WorkflowBlueprintCreator{Manager: blueprintMgr}
	mgr.BlueprintGet = blueprintMgr
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "conditions.NewDefaultRegistry", err)
	mgr.SetConditionRegistry(reg)
	return mgr, sess.ID, projectDir
}

func TestStateStartRequiresHumanApproval(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	_, err := mgr.Start(context.Background(), sessionID, api.StartWorkflowRunRequest{
		WorkflowID:      "plan",
		WorkflowVersion: "1.0.0",
	})
	if !errors.Is(err, ErrWorkflowStartRequiresHumanApproval) {
		t.Fatalf("Start err = %v want WORKFLOW_START_REQUIRES_HUMAN_APPROVAL", err)
	}
}

func TestSessionScaffoldRejectsWrongJSONShape(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	scaffold := mgr.SessionScaffold.(*SessionScaffoldSQLStore)
	err := scaffold.queries.UpsertSessionScaffoldVars(context.Background(), db.UpsertSessionScaffoldVarsParams{
		SessionID: sessionID,
		VarsJson:  "[]",
		UpdatedAt: db.FormatTime(time.Now().UTC()),
	})
	testutil.FailErr(t, "UpsertSessionScaffoldVars", err)
	if _, err := scaffold.GetVars(context.Background(), sessionID); err == nil {
		t.Fatal("GetVars accepted a non-object JSON value")
	}
}

func TestStateStartAfterSlashNoExtraApproval(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	_, handled, err := mgr.TrySlashPrompt(context.Background(), sessionID, "/plan", "")
	testutil.FailErr(t, "mgr.TrySlashPrompt failed", err)
	if !handled {
		t.Fatal("expected /plan slash handled")
	}
	active, err := mgr.GetActive(context.Background(), sessionID)
	testutil.FailErr(t, "mgr.GetActive failed", err)
	if active.BlueprintPath == "" {
		t.Fatal("expected blueprint_path on slash-started run")
	}
}

func TestSlashStartSupersedesReviewedCatalogRun(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	first, err := mgr.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID:      "plan",
		WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "mgr.StartHuman", err)

	_, handled, err := mgr.TrySlashPrompt(ctx, sessionID, "/options", "")
	testutil.FailErr(t, "mgr.TrySlashPrompt", err)
	if !handled {
		t.Fatal("expected /options slash handled")
	}
	prior, err := mgr.Get(ctx, first.ID)
	testutil.FailErr(t, "mgr.Get first", err)
	if prior.Status != api.WorkflowRunStatusCanceled {
		t.Fatalf("first status = %q want canceled", prior.Status)
	}
	active, err := mgr.GetActive(ctx, sessionID)
	testutil.FailErr(t, "mgr.GetActive", err)
	if active == nil || active.WorkflowID != "options" {
		t.Fatalf("active = %+v, want options", active)
	}
}

func TestSlashStartBlueprintTitleUsesCurrentIntent(t *testing.T) {
	mgr, sessionID, projectDir := testWorkflowManager(t)
	ctx := context.Background()
	_, handled, err := mgr.TrySlashPrompt(ctx, sessionID, "/options Choose package or CLI", "")
	testutil.FailErr(t, "mgr.TrySlashPrompt", err)
	if !handled {
		t.Fatal("expected /options slash handled")
	}
	active, err := mgr.GetActive(ctx, sessionID)
	testutil.FailErr(t, "mgr.GetActive", err)
	content, err := ReadBlueprintFile(projectDir, active.BlueprintPath)
	testutil.FailErr(t, "ReadBlueprintFile", err)
	title, ok := blueprintfile.DeclaredTitle(content)
	if !ok || title != blueprint.SlugTitle("Choose package or CLI") {
		t.Fatalf("Blueprint title = %q ok=%v", title, ok)
	}
}

func TestHumanStartAfterProposal(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	_, err := mgr.StartAmbient(context.Background(), sessionID, "implement", "1.0.0")
	testutil.FailErr(t, "mgr.StartAmbient failed", err)
	if err := mgr.NoteWorkflowStartProposal(context.Background(), sessionID, "plan", "1.0.0"); err != nil {
		testutil.FailErr(t, "mgr.NoteWorkflowStartProposal failed", err)
	}
	run, err := mgr.StartHuman(context.Background(), sessionID, api.StartWorkflowRunRequest{
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
	if err := mgr.NoteWorkflowStartProposal(context.Background(), sessionID, "plan", "1.0.0"); err != nil {
		testutil.FailErr(t, "mgr.NoteWorkflowStartProposal failed", err)
	}
	for _, msg := range []string{"yes, start plan", "go ahead", "lgtm", "approved"} {
		testutil.FailErr(t, "TryResolveUserFeedback", mgr.TryResolveUserFeedback(context.Background(), sessionID, "", testutil.HostOwner().ID, msg))
	}
	_, err := mgr.Start(context.Background(), sessionID, api.StartWorkflowRunRequest{
		WorkflowID:      "plan",
		WorkflowVersion: "1.0.0",
	})
	if !errors.Is(err, ErrWorkflowStartRequiresHumanApproval) {
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
