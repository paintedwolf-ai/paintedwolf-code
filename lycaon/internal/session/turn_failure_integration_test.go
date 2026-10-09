//go:build integration

package session_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	loopwake "github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
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
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// silentProviderClient reports an exhausted silent request.
type silentProviderClient struct{}

func (silentProviderClient) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return nil, &failure.ProviderSilentError{ProviderID: "together-1", Attempts: 2, Silence: 30 * time.Second}
}

func (silentProviderClient) Stream(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	return nil, &failure.ProviderSilentError{ProviderID: "together-1", Attempts: 2, Silence: 30 * time.Second}
}

// Asynchronous host-turn failures surface through the session sink.
func TestFailedHostTurnReportsItself(t *testing.T) {
	root := configlayout.FindModuleRoot()
	sqlDB := testdbfixture.Open(t, "host-turn-failure.db")

	st := store.NewSQL(sqlDB)
	mgr := session.NewManager(st, silentProviderClient{}, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)
	mgr.SetAgentRegistry(agents)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	testutil.FailErr(t, "install anchor registry", mgr.InstallAnchorRegistry())

	var mu sync.Mutex
	var reported []error
	mgr.SetTurnFailureSink(func(_ context.Context, _ string, err error) {
		mu.Lock()
		reported = append(reported, err)
		mu.Unlock()
	})

	wfStore := workflowpersistence.New(sqlDB)
	bundledDir := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "workflows")
	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	wfMgr := workflow.NewManager(wfStore, st, manifestRegistry, nil)
	mgr.SetWorkflowDomains(&session.WorkflowDomains{Runs: wfMgr.Store.Runs, Policy: wfMgr.Policy, Ambient: wfMgr.Ambient, Blueprints: wfMgr.Blueprints, Batch: wfMgr.Batch, Slash: wfMgr.Slash, Requests: wfMgr.Requests, Feedback: wfMgr.Feedback, Transcript: wfMgr.Transcript, Asks: wfMgr.Asks, Fanout: wfMgr.Fanout, Phases: wfMgr.Phases, Reports: wfMgr.Reports, Recovery: wfMgr.Recovery, Cleanup: wfMgr})
	mgr.SetLoopWorkflowSource(&loopwake.WorkflowDomains{Runs: wfMgr.Store.Runs, Approvals: wfMgr.Policy, Obligations: wfMgr.Obligations})

	ctx := context.Background()
	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := st.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	ref, err := workflowdef.LoadRegistryConfig(extpacks.OnDisk(bundledDir))
	testutil.FailErr(t, "LoadRegistryConfig", err)
	_, err = wfMgr.Ambient.StartAmbient(ctx, sess.ID, ref.ID, ref.Version)
	testutil.FailErr(t, "StartAmbient", err)
	testutil.FailErr(t, "set session busy", st.SetSessionStatus(ctx, sess.ID, wire.SessionStatusBusy))

	finishExecution := mgr.BeginPromptExecutionForTest(t.Context(), sess.ID)
	mgr.NudgeCoordinatorLoop(ctx, sess.ID, anchor.LegFinished, anchor.LegFinished,
		runstate.ImplementWorkLegKey(sess.ID), anchor.Envelope{})
	if _, ok := mgr.PendingLoopNudgeForTest(sess.ID); !ok {
		t.Fatal("expected a deferred loop wake while prompt execution is active")
	}
	finishExecution()
	mgr.DrainLoopPendingForTest(ctx, sess.ID)

	testutil.WaitFor(t, 10*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(reported) > 0
	})

	mu.Lock()
	defer mu.Unlock()
	silent, ok := failure.AsProviderSilent(reported[0])
	if !ok {
		t.Fatalf("want the provider fault to survive to the sink, got %T: %v", reported[0], reported[0])
	}
	if silent.ProviderID != "together-1" {
		t.Fatalf("want the failing provider named, got %q", silent.ProviderID)
	}
}
