package security

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	workflowstatetools "github.com/lycaon/lycaon/internal/workflow/statetools"
	"github.com/lycaon/lycaon/internal/workflow/toolguard"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestStateStartRespectsProjectDir(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "wf.db")

	wfStore := workflowpersistence.New(sqlDB)
	sessStore := storetest.New(t, testdbseed.DefaultProjectID)
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "workflow.RegistryFromDirs failed", err)
	mgr := workflow.NewManager(wfStore, sessStore, reg, nil)
	toolReg := tools.NewDefaultRegistry()
	if err := workflowstatetools.RegisterStateTools(toolReg, workflowstatetools.StateToolDeps{Resolver: &mgr.Resolver, Starts: mgr.Starts, Scaffold: mgr.Blueprints.Scaffold, Sessions: sessStore.Sessions}); err != nil {
		testutil.FailErr(t, "workflow.RegisterStateTools failed", err)
	}

	dirA := t.TempDir()
	dirB := t.TempDir()
	sess, err := sessStore.Sessions.Create(context.Background(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "sessStore.Create failed", err)
	testdbseed.BindSessionWorkspace(t, sessStore.DB(), sess.ID, dirA)

	out, err := toolReg.Run(context.Background(), "state_start", map[string]any{"workflow_id": "implement", "workflow_version": "1.0.0"}, SecurityToolContext(sess.ID, dirB, ""))
	if !errors.Is(err, toolguard.ErrProjectMismatch) || out != "" {
		t.Fatalf("state_start outside the session project = %q, %v; want project_dir mismatch error", out, err)
	}
}
