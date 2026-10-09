package security

import (
	"context"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	workflowstatetools "github.com/lycaon/lycaon/internal/workflow/statetools"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestStateToolsRespectProjectDir(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "wf.db")

	wfStore := workflowpersistence.New(sqlDB)
	sessStore := store.NewMemory()
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "workflow.RegistryFromDirs failed", err)
	mgr := workflow.NewManager(wfStore, sessStore, reg, nil)
	toolReg := tools.NewDefaultRegistry()
	if err := workflowstatetools.RegisterStateTools(toolReg, workflowstatetools.StateToolDeps{Runs: mgr.Store.Runs, Vars: mgr.Phases.Vars, Journal: mgr.Phases.Journal, Resolver: &mgr.Resolver, Starts: mgr.Starts, Controls: mgr.Controls, Scaffold: mgr.Blueprints.Scaffold, Sessions: sessStore}); err != nil {
		testutil.FailErr(t, "workflow.RegisterStateTools failed", err)
	}

	dirA := t.TempDir()
	dirB := t.TempDir()
	sess, err := sessStore.Create(context.Background(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "sessStore.Create failed", err)
	testdbseed.BindSessionWorkspace(t, sessStore, sess.ID, dirA)

	_, err = toolReg.Run(context.Background(), "state_query", nil, securityToolContext(sess.ID, dirB, ""))
	if err == nil {
		t.Fatal("expected project_dir mismatch error")
	}
}
