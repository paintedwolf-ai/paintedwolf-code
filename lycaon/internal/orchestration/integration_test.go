package orchestration_test

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMultiLegDelegationDependencyOrder(t *testing.T) {
	orch, rec, sessMgr, sessStore := newPipelineIntegrationOrchestrator(t)
	ctx := context.Background()

	spec, err := orchestration.LoadTopologyFromFile(bundledTopologyPath(t, "default-pipeline.yaml"))
	testutil.FailErr(t, "orchestration.LoadTopologyFromFile failed", err)
	spec.Task = "multi-leg order"

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	if _, err := orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  *spec,
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	}); err != nil {
		testutil.FailErr(t, "orch.Run failed", err)
	}
	if len(rec.order) != 6 {
		t.Fatalf("dispatch count = %d want 6", len(rec.order))
	}

	researchIdx := indexOf(rec.order, "research")
	planIdx := indexOf(rec.order, "plan")
	implementIdx := indexOf(rec.order, "implement")
	reviewIdx := indexOf(rec.order, "review")
	testIdx := indexOf(rec.order, "test")
	closeoutIdx := indexOf(rec.order, "closeout")
	for name, idx := range map[string]int{
		"research": researchIdx, "plan": planIdx, "implement": implementIdx,
		"review": reviewIdx, "test": testIdx, "closeout": closeoutIdx,
	} {
		if idx < 0 {
			t.Fatalf("missing stage %q in %v", name, rec.order)
		}
	}
	if researchIdx >= planIdx || planIdx >= implementIdx {
		t.Fatalf("sequential order violated: %v", rec.order)
	}
	if reviewIdx <= implementIdx || testIdx <= implementIdx {
		t.Fatalf("review/test before implement: %v", rec.order)
	}
	if closeoutIdx <= reviewIdx || closeoutIdx <= testIdx {
		t.Fatalf("closeout before parallel pair: %v", rec.order)
	}
}

func TestParallelLegsStartConcurrently(t *testing.T) {
	orch, rec, sessMgr, sessStore := newPipelineIntegrationOrchestrator(t)
	// Neither parallel stage settles until both have entered DispatchUnit.
	rec.barrierSet = map[string]bool{"review": true, "test": true}
	ctx := t.Context()

	spec, err := orchestration.LoadTopologyFromFile(bundledTopologyPath(t, "default-pipeline.yaml"))
	testutil.FailErr(t, "orchestration.LoadTopologyFromFile failed", err)

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	if _, err := orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  *spec,
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	}); err != nil {
		testutil.FailErr(t, "orch.Run failed", err)
	}
	if rec.barrierSeen != 2 {
		t.Fatalf("parallel barrier seen = %d want 2", rec.barrierSeen)
	}
}

func TestDependencyFailureBlocksDownstream(t *testing.T) {
	mockCfg, err := llm.LoadMockConfig()
	testutil.FailErr(t, "llm.LoadMockConfig failed", err)

	sessStore := storetest.New(t, testdbseed.DefaultProjectID)
	sessMgr := session.NewHost(sessStore, session.Models{Client: llm.NewMockProvider(mockCfg), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	failRec := &recordingWorkers{
		TopologyWorkers: orchestration.NewMemoryTopologyWorkers(),
		failStage:       "research",
		order:           make([]string, 0, 4),
	}

	reg := loadAgentRegistryFromConfig(t)
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Workers: failRec,
		Agents:  reg,
	})

	ctx := context.Background()
	spec, err := orchestration.LoadTopologyFromFile(bundledTopologyPath(t, "default-pipeline.yaml"))
	testutil.FailErr(t, "orchestration.LoadTopologyFromFile failed", err)

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  *spec,
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	if err == nil {
		t.Fatal("expected pipeline error when research fails")
	}
	if slices.Contains(failRec.order, "plan") {
		t.Fatalf("plan dispatched after research failure: %v", failRec.order)
	}
}
