//go:build integration

package session_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
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
	wire "github.com/lycaon/lycaon/pkg/api"
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
	mgr.Profiles.SetAgentRegistry(agents)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	testutil.FailErr(t, "install anchor registry", mgr.Guidance.InstallAnchorRegistry())

	var mu sync.Mutex
	var reported []error
	mgr.Runner.Turns.SetFailureSink(func(_ context.Context, _ string, err error) {
		mu.Lock()
		reported = append(reported, err)
		mu.Unlock()
	})

	wfStore := workflow.NewSQLStore(sqlDB)
	bundledDir := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "workflows")
	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	wfMgr := workflow.NewManager(wfStore, st, manifestRegistry, nil)
	wfMgr.Resolver = workflow.ManifestResolver{}
	mgr.SetWorkflowSessionView(wfMgr)
	mgr.SetLoopWorkflowSource(wfMgr)

	ctx := context.Background()
	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := st.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	ref, err := workflowdef.LoadRegistryConfig(extpacks.OnDisk(bundledDir))
	testutil.FailErr(t, "LoadRegistryConfig", err)
	_, err = wfMgr.StartAmbient(ctx, sess.ID, ref.ID, ref.Version)
	testutil.FailErr(t, "StartAmbient", err)
	testutil.FailErr(t, "set session busy", st.SetSessionStatus(ctx, sess.ID, wire.SessionStatusBusy))

	finishExecution := mgr.Runner.Coordinator.CoordinatorLoop().BeginPromptExecution(t.Context(), sess.ID)
	mgr.NudgeCoordinatorLoop(ctx, sess.ID, anchor.LegFinished, anchor.LegFinished,
		workflow.ImplementWorkLegKey(sess.ID), anchor.Envelope{})
	if _, ok := mgr.Runner.Coordinator.CoordinatorLoop().PendingForTest(sess.ID); !ok {
		t.Fatal("expected a deferred loop wake while prompt execution is active")
	}
	finishExecution()
	mgr.Runner.Coordinator.CoordinatorLoop().DrainPending(ctx, sess.ID)

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
