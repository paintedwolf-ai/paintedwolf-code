package orchestration_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func newSupervisorTestOrchestrator(t *testing.T, del orchestration.PipelineDelegation) (*orchestration.OrchestratorImpl, *delegation.MemoryStore, *session.Manager, *store.Memory) {
	t.Helper()
	delStore := delegation.NewMemoryStore()
	sessStore := store.NewMemory()
	sessMgr := session.NewManager(sessStore, llm.NewMockProvider(nil), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	queue := worker.NewInMemoryQueue(10)
	delMgr := delegation.NewManager(delStore, queue, sessMgr, delegation.AllowGate{})
	if del == nil {
		del = &recordingDelegation{inner: delMgr, store: delStore}
	} else if rec, ok := del.(*recordingDelegation); ok {
		rec.inner = delMgr
		rec.store = delStore
	} else if rec, ok := del.(*timestampRecording); ok {
		rec.inner = delMgr
		rec.store = delStore
	}
	reg := orchestration.NewMemoryAgentRegistryForTest()
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Delegation: del,
		Store:      delStore,
		Agents:     reg,
	})
	return orch, delStore, sessMgr, sessStore
}

func supervisorSpec(profileIDs ...string) orchestration.TopologySpec {
	return orchestration.TopologySpec{
		Pattern: orchestration.TopologySupervisor,
		Task:    "supervised task",
		Supervisor: &orchestration.SupervisorSpec{
			Strategy:   orchestration.TeamStrategyParallel,
			ProfileIDs: profileIDs,
			MaxAgents:  orchestration.MaxTeamAgents,
		},
	}
}

func TestSupervisorTopologyParentLinks(t *testing.T) {
	ctx := context.Background()
	rec := &recordingDelegation{}
	orch, store, sessMgr, sessStore := newSupervisorTestOrchestrator(t, rec)

	sess, err := sessMgr.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  supervisorSpec(orchestration.ProfileCoordinator, orchestration.ProfileImplementer, orchestration.ProfileCodeReviewer),
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	testutil.FailErr(t, "orch.Run failed", err)

	delegationID, ok := store.DelegationBySessionID(sess.ID)
	if !ok {
		t.Fatal("missing delegation")
	}
	legs, err := store.ListLegs(ctx, delegationID)
	testutil.FailErr(t, "store.ListLegs failed", err)

	var supervisorID string
	for _, leg := range legs {
		if leg.Title == "Supervisor" {
			supervisorID = leg.ID
			if leg.ParentID != "" {
				t.Fatalf("supervisor leg should not have parent, got %q", leg.ParentID)
			}
			break
		}
	}
	if supervisorID == "" {
		t.Fatal("supervisor leg not found")
	}

	workers := 0
	for _, leg := range legs {
		if leg.Title == "Supervisor" {
			continue
		}
		workers++
		if leg.ParentID != supervisorID {
			t.Fatalf("worker %q parent = %q want supervisor %q", leg.Title, leg.ParentID, supervisorID)
		}
	}
	if workers != 2 {
		t.Fatalf("workers = %d want 2", workers)
	}
}

func TestSupervisorMaxAgentsRejected(t *testing.T) {
	ctx := context.Background()
	orch, _, sessMgr, sessStore := newSupervisorTestOrchestrator(t, nil)

	sess, err := sessMgr.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	ids := []string{
		"coordinator", "implementer", "code-reviewer",
		"repo-researcher", "path-explorer", "plan-writer",
	}
	spec := orchestration.TopologySpec{
		Pattern: orchestration.TopologySupervisor,
		Task:    "too many agents",
		Supervisor: &orchestration.SupervisorSpec{
			Strategy:   orchestration.TeamStrategyParallel,
			ProfileIDs: ids,
			MaxAgents:  orchestration.MaxTeamAgents,
		},
	}

	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  spec,
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	if err == nil {
		t.Fatal("expected max agents error")
	}
}

func TestSupervisorStrategyParallel(t *testing.T) {
	ctx := context.Background()
	rec := &recordingDelegation{order: make([]string, 0, 4)}
	orch, _, sessMgr, sessStore := newSupervisorTestOrchestrator(t, rec)

	sess, err := sessMgr.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  supervisorSpec(orchestration.ProfileCoordinator, orchestration.ProfileImplementer, orchestration.ProfileCodeReviewer),
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	testutil.FailErr(t, "orch.Run failed", err)
	if len(rec.order) != 3 {
		t.Fatalf("dispatch count = %d want 3", len(rec.order))
	}
	if rec.order[0] != "Supervisor" {
		t.Fatalf("first dispatch = %q want Supervisor", rec.order[0])
	}
	dispatched := map[string]bool{rec.order[1]: true, rec.order[2]: true}
	if !dispatched[orchestration.ProfileImplementer] || !dispatched[orchestration.ProfileCodeReviewer] {
		t.Fatalf("expected worker dispatches, got %v", rec.order)
	}
}

func TestSupervisorWorkersDispatchInParallel(t *testing.T) {
	ctx := context.Background()
	rec := &timestampRecording{}
	orch, _, sessMgr, sessStore := newSupervisorTestOrchestrator(t, rec)

	sess, err := sessMgr.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  supervisorSpec(orchestration.ProfileCoordinator, orchestration.ProfileImplementer, orchestration.ProfileCodeReviewer),
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	testutil.FailErr(t, "orch.Run failed", err)

	t1, ok1 := rec.timestamps[orchestration.ProfileImplementer]
	t2, ok2 := rec.timestamps[orchestration.ProfileCodeReviewer]
	if !ok1 || !ok2 {
		t.Fatalf("missing worker timestamps: %v", rec.timestamps)
	}
	delta := t1.Sub(t2)
	if delta < 0 {
		delta = -delta
	}
	if delta > 100*time.Millisecond {
		t.Fatalf("worker dispatches not parallel enough: delta=%v", delta)
	}
}

func TestSupervisorParallelYamlMockRun(t *testing.T) {
	orch, rec, sessMgr, sessStore := newPipelineIntegrationOrchestrator(t)
	ctx := context.Background()

	spec, err := orch.LoadTopology(ctx, bundledTopologyPath(t, "supervisor-parallel.yaml"))
	testutil.FailErr(t, "orch.LoadTopology failed", err)

	sess, err := sessMgr.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	result, err := orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  *spec,
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	testutil.FailErr(t, "orch.Run failed", err)
	if len(rec.order) != 3 {
		t.Fatalf("dispatch count = %d want 3", len(rec.order))
	}
	if result.FinalOutput == "" {
		t.Fatal("expected final output")
	}
}

type timestampRecording struct {
	recordingDelegation
	mu         sync.Mutex
	timestamps map[string]time.Time
}

func (r *timestampRecording) DispatchLeg(ctx context.Context, delegationID, legID, sourceToolCallID string) (*api.Leg, error) {
	leg, err := r.store.GetLeg(ctx, delegationID, legID)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.timestamps == nil {
		r.timestamps = make(map[string]time.Time)
	}
	r.timestamps[leg.Title] = time.Now()
	r.mu.Unlock()
	return r.recordingDelegation.DispatchLeg(ctx, delegationID, legID, sourceToolCallID)
}
