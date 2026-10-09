//go:build integration

package session_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
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
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestFinishPromptExecutionDrainsLoopPendingAfterCanceledRequestCtx(t *testing.T) {
	root := configlayout.FindModuleRoot()
	sqlDB := testdbfixture.Open(t, "finish-prompt-drain.db")

	store := store.NewSQL(sqlDB)
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "follow-up"}}}))
	mgr := session.NewManager(store, rec, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)
	mgr.Profiles.SetAgentRegistry(agents)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	testutil.FailErr(t, "install anchor registry", mgr.Guidance.InstallAnchorRegistry())

	wfStore := workflow.NewSQLStore(sqlDB)
	bundledDir := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "workflows")
	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	wfMgr := workflow.NewManager(wfStore, store, manifestRegistry, nil)
	wfMgr.Resolver = workflow.ManifestResolver{}
	mgr.SetWorkflowSessionView(wfMgr)
	mgr.SetLoopWorkflowSource(wfMgr)

	ctx := context.Background()
	dir := t.TempDir()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	ref, err := workflowdef.LoadRegistryConfig(extpacks.OnDisk(bundledDir))
	testutil.FailErr(t, "LoadRegistryConfig", err)
	run, err := wfMgr.StartAmbient(ctx, sess.ID, ref.ID, ref.Version)
	testutil.FailErr(t, "StartAmbient", err)
	_ = run
	if err := store.SetSessionStatus(ctx, sess.ID, wire.SessionStatusBusy); err != nil {
		testutil.FailErr(t, "store.SetSessionStatus failed", err)
	}

	finishExecution := mgr.Runner.Coordinator.CoordinatorLoop().BeginPromptExecution(t.Context(), sess.ID)
	mgr.NudgeCoordinatorLoop(ctx, sess.ID, anchor.LegFinished, anchor.LegFinished, workflow.ImplementWorkLegKey(sess.ID), anchor.Envelope{})
	if _, ok := mgr.Runner.Coordinator.CoordinatorLoop().PendingForTest(sess.ID); !ok {
		t.Fatal("expected deferred loop wake while prompt execution is active")
	}
	finishExecution()

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	func() {
		_ = mgr.Runner.Settlement.Finish(canceled, sess.ID, true, true, "")
		_ = mgr.Runner.Settlement.Drain(canceled, sess.ID)
	}()

	testutil.WaitFor(t, 5*time.Second, func() bool {
		msgs, err := store.GetMessages(ctx, sess.ID)
		if err != nil {
			return false
		}
		for _, msg := range msgs {
			if msg.Kind == wire.MessageKindHostLoopWake {
				return true
			}
		}
		return false
	})
}
