//go:build integration

package session_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	loopwake "github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	wire "github.com/lycaon/lycaon/pkg/api"
	"path/filepath"
	"testing"
)

func TestFinishPromptExecutionQueuesOverlayIntegrateCompleteKick(t *testing.T) {
	root := configlayout.FindModuleRoot()
	sqlDB := testdbfixture.Open(t, "overlay-integrate-kick.db")

	store := store.NewSQL(sqlDB)
	mgr := session.NewManager(store, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	agents := orchestration.NewMemoryAgentRegistry()
	testutil.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(context.Background(), agents))
	mgr.SetAgentRegistry(agents)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	testutil.FailErr(t, "install anchor registry", mgr.InstallAnchorRegistry())

	wfStore := workflowpersistence.New(sqlDB)
	bundledDir := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "workflows")
	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	wfMgr := workflow.NewManager(wfStore, store, manifestRegistry, nil)
	mgr.SetWorkflowDomains(&session.WorkflowDomains{Runs: wfMgr.Store.Runs, Policy: wfMgr.Policy, Ambient: wfMgr.Ambient, Blueprints: wfMgr.Blueprints, Batch: wfMgr.Batch, Slash: wfMgr.Slash, Requests: wfMgr.Requests, Feedback: wfMgr.Feedback, Transcript: wfMgr.Transcript, Asks: wfMgr.Asks, Fanout: wfMgr.Fanout, Phases: wfMgr.Phases, Reports: wfMgr.Reports, Recovery: wfMgr.Recovery, Cleanup: wfMgr})
	mgr.SetLoopWorkflowSource(&loopwake.WorkflowDomains{Runs: wfMgr.Store.Runs, Approvals: wfMgr.Policy, Obligations: wfMgr.Obligations})
	prog := progress.NewMemoryStore()
	mgr.SetProgressStore(prog)

	ctx := context.Background()
	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	ref, err := workflowdef.LoadRegistryConfig(extpacks.OnDisk(bundledDir))
	testutil.FailErr(t, "LoadRegistryConfig", err)
	_, err = wfMgr.Ambient.StartAmbient(ctx, sess.ID, ref.ID, ref.Version)
	testutil.FailErr(t, "StartAmbient", err)
	prog.Set(sess.ID, "## Progress\n- [x] implement\n- [x] verify")

	testutil.FailErr(t, "ApplyCoordinatorBatchEvent dispatch", wfMgr.Batch.ApplyCoordinatorBatchEvent(ctx, sess.ID, batch.EventWriterTaskEnqueued, 0))
	testutil.FailErr(t, "ApplyCoordinatorBatchEvent integrate", wfMgr.Batch.ApplyCoordinatorBatchEvent(ctx, sess.ID, batch.EventOverlaysPendingIdle, 0))

	mgr.FinishPromptExecutionForTest(ctx, sess.ID, false, true)

	kickID, ok := mgr.PendingKickIDForTest(sess.ID)
	wantID := anchor.InformRender(anchor.OverlayPromoteComplete)
	if !ok || kickID != wantID {
		t.Fatalf("kick_id = %q ok=%v want %q", kickID, ok, wantID)
	}
}
