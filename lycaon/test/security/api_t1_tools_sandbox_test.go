package security

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestStateToolsRespectProjectDir(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "wf.db")

	wfStore := workflow.NewSQLStore(sqlDB)
	sessStore := store.NewMemory()
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "workflow.RegistryFromDirs failed", err)
	mgr := workflow.NewManager(wfStore, sessStore, reg, nil)
	toolReg := tools.NewDefaultRegistry()
	if err := workflow.RegisterStateTools(toolReg, workflow.StateToolDeps{Runs: mgr, Sessions: sessStore}); err != nil {
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
