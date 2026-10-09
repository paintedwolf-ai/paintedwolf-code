package orchestration_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func bundledTopologyPath(t *testing.T, name string) extpacks.Source {
	t.Helper()
	return extpacks.Bundled(config.PlatformFlows.Join("_topologies", name))
}

func loadAgentRegistryFromConfig(t *testing.T) *orchestration.MemoryAgentRegistry {
	t.Helper()
	reg := orchestration.NewMemoryAgentRegistry()
	if err := orchestration.LoadRequiredAgentRegistry(context.Background(), reg); err != nil {
		testutil.FailErr(t, "LoadRequiredAgentRegistry", err)
	}
	if err := orchestration.ValidateGateAgents(reg); err != nil {
		testutil.FailErr(t, "orchestration.ValidateGateAgents failed", err)
	}
	return reg
}

var _ orchestration.Orchestrator = (*orchestration.OrchestratorImpl)(nil)

func TestPipelineTopologyLegAgentTypes(t *testing.T) {
	ctx := context.Background()
	delStore := delegation.NewMemoryStore()
	sessStore := store.NewMemory()
	sessMgr := session.NewManager(sessStore, llm.NewMockProvider(nil), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	queue := worker.NewInMemoryQueue(10)
	delMgr := delegation.NewManager(delStore, queue, sessMgr, delegation.AllowGate{})

	reg := orchestration.NewMemoryAgentRegistryForTest()
	rec := &recordingDelegation{inner: delMgr, store: delStore}
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Delegation: rec,
		Store:      delStore,
		Agents:     reg,
	})

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	spec := orchestration.TopologySpec{
		Pattern: orchestration.TopologyPipeline,
		Task:    "build feature",
		Pipeline: &orchestration.PipelineSpec{
			Stages: []orchestration.PipelineStage{
				{Name: "research", AgentProfile: orchestration.ProfileRepoResearcher},
				{Name: "plan", AgentProfile: orchestration.ProfileCoordinator, InputFrom: []string{"research"}},
			},
		},
	}

	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  spec,
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	testutil.FailErr(t, "orch.Run failed", err)
	if len(rec.legs) != 2 {
		t.Fatalf("recorded legs = %d want 2", len(rec.legs))
	}
	want := map[string]string{
		"research": orchestration.ProfileRepoResearcher,
		"plan":     orchestration.ProfileCoordinator,
	}
	for stage, wantAgent := range want {
		leg, ok := rec.legs[stage]
		if !ok {
			t.Fatalf("missing leg for stage %q", stage)
		}
		if leg.AgentType != wantAgent {
			t.Fatalf("stage %q agent = %q want %q", stage, leg.AgentType, wantAgent)
		}
		if leg.Title != stage {
			t.Fatalf("stage %q title = %q", stage, leg.Title)
		}
	}
}

func TestPipelineUnknownProfileFailsRun(t *testing.T) {
	ctx := context.Background()
	delStore := delegation.NewMemoryStore()
	sessStore := store.NewMemory()
	sessMgr := session.NewManager(sessStore, llm.NewMockProvider(nil), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	queue := worker.NewInMemoryQueue(10)
	delMgr := delegation.NewManager(delStore, queue, sessMgr, delegation.AllowGate{})

	reg := orchestration.NewMemoryAgentRegistryForTest()
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Delegation: delMgr,
		Store:      delStore,
		Agents:     reg,
	})

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	spec := orchestration.TopologySpec{
		Pattern: orchestration.TopologyPipeline,
		Task:    "task",
		Pipeline: &orchestration.PipelineSpec{
			Stages: []orchestration.PipelineStage{
				{Name: "broken", AgentProfile: "missing-agent"},
			},
		},
	}

	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  spec,
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	if err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

func newPipelineIntegrationOrchestrator(t *testing.T) (*orchestration.OrchestratorImpl, *recordingDelegation, *session.Manager, *store.Memory) {
	t.Helper()
	mockCfg, err := llm.LoadMockConfig()
	testutil.FailErr(t, "llm.LoadMockConfig failed", err)

	delStore := delegation.NewMemoryStore()
	sessStore := store.NewMemory()
	sessMgr := session.NewManager(sessStore, llm.NewMockProvider(mockCfg), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	queue := worker.NewInMemoryQueue(10)
	delMgr := delegation.NewManager(delStore, queue, sessMgr, delegation.AllowGate{})
	rec := &recordingDelegation{inner: delMgr, store: delStore, order: make([]string, 0, 8)}

	reg := loadAgentRegistryFromConfig(t)
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Delegation: rec,
		Store:      delStore,
		Agents:     reg,
	})
	return orch, rec, sessMgr, sessStore
}

func TestPipelineSequentialDispatchOrder(t *testing.T) {
	orch, rec, sessMgr, sessStore := newPipelineIntegrationOrchestrator(t)
	ctx := context.Background()

	spec, err := orchestration.LoadTopologyFromFile(bundledTopologyPath(t, "default-pipeline.yaml"))
	testutil.FailErr(t, "orchestration.LoadTopologyFromFile failed", err)
	spec.Task = "integration pipeline"

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	result, err := orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  *spec,
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	testutil.FailErr(t, "orch.Run failed", err)
	if result == nil || len(result.StageOutputs) != 6 {
		t.Fatalf("stage outputs = %d", len(result.StageOutputs))
	}

	researchIdx := indexOf(rec.order, "research")
	planIdx := indexOf(rec.order, "plan")
	implementIdx := indexOf(rec.order, "implement")
	closeoutIdx := indexOf(rec.order, "closeout")
	if researchIdx < 0 || planIdx < 0 || implementIdx < 0 || closeoutIdx < 0 {
		t.Fatalf("dispatch order missing stage: %v", rec.order)
	}
	if researchIdx >= planIdx || planIdx >= implementIdx || implementIdx >= closeoutIdx {
		t.Fatalf("bad dispatch order: %v", rec.order)
	}
}

func TestPipelineReviewTestUnlockAfterImplement(t *testing.T) {
	orch, rec, sessMgr, sessStore := newPipelineIntegrationOrchestrator(t)
	ctx := context.Background()

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
		t.Fatal(err)
	}

	implementIdx := indexOf(rec.order, "implement")
	reviewIdx := indexOf(rec.order, "review")
	testIdx := indexOf(rec.order, "test")
	if implementIdx < 0 || reviewIdx < 0 || testIdx < 0 {
		t.Fatalf("dispatch order missing stage: %v", rec.order)
	}
	if reviewIdx <= implementIdx || testIdx <= implementIdx {
		t.Fatalf("review/test dispatched before implement: %v", rec.order)
	}

	pair := []string{rec.order[implementIdx+1], rec.order[implementIdx+2]}
	slices.Sort(pair)
	want := []string{"review", "test"}
	if !slices.Equal(pair, want) {
		t.Fatalf("after implement expected review+test, got %v in %v", pair, rec.order)
	}
}

func TestPipelineFullDefaultPipelineMockRun(t *testing.T) {
	orch, rec, sessMgr, sessStore := newPipelineIntegrationOrchestrator(t)
	ctx := context.Background()

	spec, err := orch.LoadTopology(ctx, bundledTopologyPath(t, "default-pipeline.yaml"))
	testutil.FailErr(t, "orch.LoadTopology failed", err)

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	result, err := orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  *spec,
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	testutil.FailErr(t, "orch.Run failed", err)
	if len(rec.order) != 6 {
		t.Fatalf("dispatch count = %d want 6", len(rec.order))
	}
	if result.FinalOutput == "" {
		t.Fatal("expected final output")
	}
}

type recordingDelegation struct {
	inner        orchestration.PipelineDelegation
	store        orchestration.PipelineDelegationStore
	mu           sync.Mutex
	legs         map[string]api.Leg
	order        []string
	dispatchAt   map[string]time.Time
	failStage    string
	dispatched   chan<- struct{}
	holdOutcomes <-chan struct{}
	settled      chan<- struct{}
	holdReturns  <-chan struct{}
}

func (r *recordingDelegation) DispatchLeg(ctx context.Context, delegationID, legID, sourceToolCallID string) (*api.Leg, error) {
	leg, err := r.store.GetLeg(ctx, delegationID, legID)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.legs == nil {
		r.legs = make(map[string]api.Leg)
	}
	r.legs[leg.Title] = *leg
	r.order = append(r.order, leg.Title)
	if r.dispatchAt == nil {
		r.dispatchAt = make(map[string]time.Time)
	}
	r.dispatchAt[leg.Title] = time.Now()
	r.mu.Unlock()
	updated, err := r.inner.DispatchLeg(ctx, delegationID, legID, sourceToolCallID)
	if err != nil {
		return nil, err
	}
	if r.dispatched != nil {
		r.dispatched <- struct{}{}
	}
	if r.holdOutcomes != nil {
		select {
		case <-r.holdOutcomes:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if recorder, ok := r.inner.(interface {
		RecordOutcome(context.Context, string, string, string, api.WorkerResult) error
	}); ok {
		status := "complete"
		if leg.Title == r.failStage {
			status = "failed"
		}
		if err := recorder.RecordOutcome(ctx, delegationID, legID, updated.WorkerID, api.WorkerResult{Status: status, Summary: leg.Title + " done"}); err != nil {
			return nil, err
		}
	}
	if r.settled != nil {
		r.settled <- struct{}{}
	}
	if r.holdReturns != nil {
		select {
		case <-r.holdReturns:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return updated, nil
}

func (r *recordingDelegation) Abort(ctx context.Context, delegationID, reason string) error {
	return r.inner.Abort(ctx, delegationID, reason)
}

func indexOf(items []string, target string) int {
	for i, v := range items {
		if v == target {
			return i
		}
	}
	return -1
}
