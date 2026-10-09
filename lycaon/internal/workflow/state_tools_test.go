package workflow

import (
	"context"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	workflowstatetools "github.com/lycaon/lycaon/internal/workflow/statetools"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestStateQueryReturnsVars(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "wf.db")

	wfStore := workflowpersistence.New(sqlDB)
	sessStore := store.NewSQL(sqlDB)
	manifestReg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs failed", err)
	mgr := NewManager(wfStore, sessStore, manifestReg, nil)

	toolReg := tools.NewDefaultRegistry()
	if err := workflowstatetools.RegisterStateTools(toolReg, workflowstatetools.StateToolDeps{Runs: mgr.Store.Runs, Vars: mgr.Phases.Vars, Journal: mgr.Phases.Journal, Resolver: &mgr.Resolver, Starts: mgr.Starts, Controls: mgr.Controls, Scaffold: mgr.Blueprints.Scaffold, Sessions: sessStore}); err != nil {
		testutil.FailErr(t, "RegisterStateTools failed", err)
	}

	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	sess, err := sessStore.Create(context.Background(), api.CreateSessionRequest{
		Posture:   api.SessionPostureBuild,
		ProjectID: testdbseed.DefaultProjectID,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "sessStore.Create failed", err)
	run, err := startRun(context.Background(), mgr, sess.ID, "implement", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	vars, err := wfStore.Runs.GetScaffoldVars(context.Background(), run.ID)
	testutil.FailErr(t, "store.GetScaffoldVars failed", err)
	vars = runstate.SetHostVar(vars, "plan.status", "draft")
	if err := wfStore.State.UpdateVars(context.Background(), run, dir, vars); err != nil {
		testutil.FailErr(t, "store.UpdateVars failed", err)
	}

	raw, err := toolReg.Run(context.Background(), "state_query", map[string]any{"path": "plan.status"}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{SessionID: sess.ID},
	})
	testutil.FailErr(t, "toolReg.Run failed", err)
	if raw == "" || raw == "{}" {
		t.Fatalf("unexpected response: %s", raw)
	}
}
