package orchestration_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/orchestration/orchestrationtest"
	"github.com/lycaon/lycaon/internal/tools/toolstest"
	"slices"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
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
	sessStore := store.NewMemory()
	sessMgr := session.NewHost(sessStore, session.Models{Client: llm.NewMockProvider(nil), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, toolstest.NewRegistry())

	reg := orchestrationtest.Registry(t)
	rec := &recordingWorkers{TopologyWorkers: orchestration.NewMemoryTopologyWorkers()}
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Workers: rec,
		Agents:  reg,
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
	if len(rec.units) != 2 {
		t.Fatalf("recorded units = %d want 2", len(rec.units))
	}
	want := map[string]string{
		"research": orchestration.ProfileRepoResearcher,
		"plan":     orchestration.ProfileCoordinator,
	}
	for stage, wantAgent := range want {
		unit, ok := rec.units[stage]
		if !ok {
			t.Fatalf("missing unit for stage %q", stage)
		}
		if unit.AgentType != wantAgent {
			t.Fatalf("stage %q agent = %q want %q", stage, unit.AgentType, wantAgent)
		}
		if unit.WorkID != stage {
			t.Fatalf("stage %q work_id = %q", stage, unit.WorkID)
		}
	}
}

func TestPipelineUnknownProfileFailsRun(t *testing.T) {
	ctx := context.Background()
	sessStore := store.NewMemory()
	sessMgr := session.NewHost(sessStore, session.Models{Client: llm.NewMockProvider(nil), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, toolstest.NewRegistry())

	reg := orchestrationtest.Registry(t)
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Workers: orchestration.NewMemoryTopologyWorkers(),
		Agents:  reg,
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

func newPipelineIntegrationOrchestrator(t *testing.T) (*orchestration.OrchestratorImpl, *recordingWorkers, *session.Host, *store.Memory) {
	t.Helper()
	mockCfg, err := llm.LoadMockConfig()
	testutil.FailErr(t, "llm.LoadMockConfig failed", err)

	sessStore := store.NewMemory()
	sessMgr := session.NewHost(sessStore, session.Models{Client: llm.NewMockProvider(mockCfg), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, toolstest.NewRegistry())
	rec := &recordingWorkers{
		TopologyWorkers: orchestration.NewMemoryTopologyWorkers(),
		order:           make([]string, 0, 8),
	}

	reg := loadAgentRegistryFromConfig(t)
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Workers: rec,
		Agents:  reg,
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
		testutil.FailErr(t, "orch.Run failed", err)
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

type recordingWorkers struct {
	orchestration.TopologyWorkers
	mu           sync.Mutex
	units        map[string]orchestration.TopologyUnit
	order        []string
	failStage    string
	failStages   map[string]bool
	dispatched   chan<- struct{}
	holdOutcomes <-chan struct{}
	settled      chan<- struct{}
	holdReturns  <-chan struct{}
	barrierSet   map[string]bool
	barrierReady chan struct{}
	barrierSeen  int
}

func (r *recordingWorkers) DispatchUnit(ctx context.Context, batchID, unitID string) (*orchestration.TopologyUnit, error) {
	unit, err := r.GetUnit(ctx, batchID, unitID)
	if err != nil {
		return nil, err
	}
	var waitBarrier <-chan struct{}
	r.mu.Lock()
	if r.units == nil {
		r.units = make(map[string]orchestration.TopologyUnit)
	}
	r.units[unit.WorkID] = *unit
	r.order = append(r.order, unit.WorkID)
	if len(r.barrierSet) > 0 && r.barrierSet[unit.WorkID] {
		if r.barrierReady == nil {
			r.barrierReady = make(chan struct{})
		}
		r.barrierSeen++
		if r.barrierSeen == len(r.barrierSet) {
			close(r.barrierReady)
		}
		waitBarrier = r.barrierReady
	}
	r.mu.Unlock()

	if r.dispatched != nil {
		r.dispatched <- struct{}{}
	}
	if waitBarrier != nil {
		select {
		case <-waitBarrier:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if r.holdOutcomes != nil {
		if _, err := r.TopologyWorkers.DispatchUnit(ctx, batchID, unitID); err != nil {
			return nil, err
		}
		select {
		case <-r.holdOutcomes:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if setter, ok := r.TopologyWorkers.(interface {
			SettleUnit(string, string, api.WorkerResult) (*orchestration.TopologyUnit, error)
		}); ok {
			status := "complete"
			if r.shouldFail(unit.WorkID) {
				status = "failed"
			}
			settled, sErr := setter.SettleUnit(batchID, unitID, api.WorkerResult{Status: status, Summary: unit.WorkID + " done"})
			if sErr != nil {
				return nil, sErr
			}
			return r.afterSettle(ctx, settled)
		}
		return r.GetUnit(ctx, batchID, unitID)
	}

	settler, ok := r.TopologyWorkers.(orchestration.TopologyDirectSettler)
	if !ok {
		return r.TopologyWorkers.DispatchUnit(ctx, batchID, unitID)
	}
	status := "complete"
	if r.shouldFail(unit.WorkID) {
		status = "failed"
	}
	updated, err := settler.DispatchSettled(ctx, batchID, unitID, api.WorkerResult{
		Status:  status,
		Summary: unit.WorkID + " done",
	})
	if err != nil {
		return nil, err
	}
	return r.afterSettle(ctx, updated)
}

func (r *recordingWorkers) shouldFail(workID string) bool {
	if workID == r.failStage {
		return true
	}
	return r.failStages[workID]
}

func (r *recordingWorkers) afterSettle(ctx context.Context, updated *orchestration.TopologyUnit) (*orchestration.TopologyUnit, error) {
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

func indexOf(items []string, target string) int {
	for i, v := range items {
		if v == target {
			return i
		}
	}
	return -1
}
