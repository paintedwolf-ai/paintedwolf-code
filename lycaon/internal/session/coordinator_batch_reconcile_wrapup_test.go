//go:build integration

package session_test

import (
	"context"
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
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	wire "github.com/lycaon/lycaon/pkg/api"
	"path/filepath"
	"testing"
)

func TestReconcileCoordinatorBatch_skipsSynthesisReadyWithoutBatchReady(t *testing.T) {
	root := configlayout.FindModuleRoot()
	sqlDB := testdbfixture.Open(t, "batch-reconcile-wrapup.db")

	store := store.NewSQL(sqlDB)
	mgr := session.NewManager(store, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	agents := orchestration.NewMemoryAgentRegistry()
	testutil.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(context.Background(), agents))
	mgr.SetAgentRegistry(agents)

	wfStore := workflowpersistence.New(sqlDB)
	bundledDir := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "workflows")
	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	wfMgr := workflow.NewManager(wfStore, store, manifestRegistry, nil)
	mgr.SetWorkflowDomains(&session.WorkflowDomains{Runs: wfMgr.Store.Runs, Policy: wfMgr.Policy, Ambient: wfMgr.Ambient, Blueprints: wfMgr.Blueprints, Batch: wfMgr.Batch, Slash: wfMgr.Slash, Requests: wfMgr.Requests, Feedback: wfMgr.Feedback, Transcript: wfMgr.Transcript, Asks: wfMgr.Asks, Fanout: wfMgr.Fanout, Phases: wfMgr.Phases, Reports: wfMgr.Reports, Recovery: wfMgr.Recovery, Cleanup: wfMgr})

	ctx := context.Background()
	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	ref, err := workflowdef.LoadRegistryConfig(extpacks.OnDisk(bundledDir))
	testutil.FailErr(t, "LoadRegistryConfig", err)
	_, err = wfMgr.Ambient.StartAmbient(ctx, sess.ID, ref.ID, ref.Version)
	testutil.FailErr(t, "StartAmbient", err)

	testutil.FailErr(t, "ApplyCoordinatorBatchEvent dispatch", wfMgr.Batch.ApplyCoordinatorBatchEvent(ctx, sess.ID, batch.EventWriterTaskEnqueued, 0))
	testutil.FailErr(t, "ApplyCoordinatorBatchEvent integrate", wfMgr.Batch.ApplyCoordinatorBatchEvent(ctx, sess.ID, batch.EventOverlaysPendingIdle, 0))

	inlineEdit := []wire.Message{
		{Role: wire.MessageRoleUser, Content: "fix handler"},
		{
			Role:      wire.MessageRoleAssistant,
			ToolCalls: []wire.ToolCall{{ID: "c1", Name: "edit", Args: map[string]any{"path": "internal/handler.go"}}},
		},
		{
			Role:    wire.MessageRoleTool,
			Content: "ok",
			ToolResult: &wire.ToolResult{
				Outcome:  wire.ToolResultOutcomeCompleted,
				FileEdit: &wire.FileEditSnapshot{Path: "internal/handler.go"},
			},
		},
	}
	testutil.FailErr(t, "append messages", store.AppendMessages(ctx, sess.ID, inlineEdit...))

	mgr.ReconcileCoordinatorBatchFromLedgerForTest(ctx, sess.ID)

	state := mgr.BuildImplementSessionState(ctx, sess)
	if state.BatchPhase == batch.PhaseSynthesize {
		t.Fatalf("batch phase = %q want not synthesize without verify pass", state.BatchPhase)
	}
	if state.BatchReadyForSynthesis {
		t.Fatal("BuildImplementSessionState must not mark batch ready before verifier pass")
	}
	if !state.WrapupGatesLoaded {
		t.Fatal("expected wrapup gates loaded on session state")
	}
}
