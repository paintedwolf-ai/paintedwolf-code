//go:build integration

package session_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestMaybeAdvanceCoordinatorBatchOnTaskEnqueued_writerStillDispatches(t *testing.T) {
	root := configlayout.FindModuleRoot()
	sqlDB := testdbfixture.Open(t, "batch-writer.db")

	store := store.NewSQL(sqlDB)
	mgr := session.NewManager(store, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	agents := orchestration.NewMemoryAgentRegistry()
	testutil.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(context.Background(), agents))
	mgr.SetAgentRegistry(agents)

	wfStore := workflow.NewSQLStore(sqlDB)
	bundledDir := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "workflows")
	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	wfMgr := workflow.NewManager(wfStore, store, manifestRegistry, nil)
	wfMgr.Resolver = workflow.ManifestResolver{}
	mgr.SetWorkflowSessionView(wfMgr)

	ctx := context.Background()
	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	ref, err := workflowdef.LoadRegistryConfig(extpacks.OnDisk(bundledDir))
	testutil.FailErr(t, "LoadRegistryConfig", err)
	_, err = wfMgr.StartAmbient(ctx, sess.ID, ref.ID, ref.Version)
	testutil.FailErr(t, "StartAmbient", err)

	mgr.MaybeAdvanceCoordinatorBatchOnTaskEnqueuedForTest(ctx, sess.ID, orchestration.ProfileImplementer)

	state := mgr.BuildImplementSessionState(ctx, sess)
	if state.BatchPhase != batch.PhaseDispatch {
		t.Fatalf("batch phase = %q want dispatch after writer enqueue", state.BatchPhase)
	}
}
