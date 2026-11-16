package orchestration_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func newSupervisorTestOrchestrator(t *testing.T, rec *recordingWorkers) (*orchestration.OrchestratorImpl, *orchestration.MemoryTopologyWorkers, *session.Host, *store.Memory) {
	t.Helper()
	mem := orchestration.NewMemoryTopologyWorkers()
	sessStore := store.NewMemory()
	sessMgr := session.NewHost(sessStore, session.Models{Client: llm.NewMockProvider(nil), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	if rec == nil {
		rec = &recordingWorkers{}
	}
	rec.TopologyWorkers = mem
	reg := orchestration.NewMemoryAgentRegistryForTest()
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Workers: rec,
		Agents:  reg,
	})
	return orch, mem, sessMgr, sessStore
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
	rec := &recordingWorkers{}
	orch, mem, sessMgr, sessStore := newSupervisorTestOrchestrator(t, rec)

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  supervisorSpec(orchestration.ProfileCoordinator, orchestration.ProfileImplementer, orchestration.ProfileCodeReviewer),
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	testutil.FailErr(t, "orch.Run failed", err)

	batchID, ok, err := mem.BatchBySession(ctx, sess.ID)
	testutil.FailErr(t, "BatchBySession failed", err)
	if !ok {
		t.Fatal("missing topology batch")
	}
	units, err := mem.ListUnits(ctx, batchID)
	testutil.FailErr(t, "mem.ListUnits failed", err)

	var supervisorID string
	for _, u := range units {
		if u.WorkID == "Supervisor" {
			supervisorID = u.ID
			if u.ParentID != "" {
				t.Fatalf("supervisor unit should not have parent, got %q", u.ParentID)
			}
			break
		}
	}
	if supervisorID == "" {
		t.Fatal("supervisor unit not found")
	}

	workers := 0
	for _, u := range units {
		if u.WorkID == "Supervisor" {
			continue
		}
		workers++
		if u.ParentID != supervisorID {
			t.Fatalf("worker %q parent = %q want supervisor %q", u.WorkID, u.ParentID, supervisorID)
		}
	}
	if workers != 2 {
		t.Fatalf("workers = %d want 2", workers)
	}
}

func TestSupervisorMaxAgentsRejected(t *testing.T) {
	ctx := context.Background()
	orch, _, sessMgr, sessStore := newSupervisorTestOrchestrator(t, nil)

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
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
	rec := &recordingWorkers{order: make([]string, 0, 4)}
	orch, _, sessMgr, sessStore := newSupervisorTestOrchestrator(t, rec)

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
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
	ctx := t.Context()
	rec := &recordingWorkers{
		barrierSet: map[string]bool{
			orchestration.ProfileImplementer:  true,
			orchestration.ProfileCodeReviewer: true,
		},
	}
	orch, _, sessMgr, sessStore := newSupervisorTestOrchestrator(t, rec)

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  supervisorSpec(orchestration.ProfileCoordinator, orchestration.ProfileImplementer, orchestration.ProfileCodeReviewer),
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	testutil.FailErr(t, "orch.Run failed", err)
	if rec.barrierSeen != 2 {
		t.Fatalf("parallel barrier seen = %d want 2", rec.barrierSeen)
	}
}

func TestSupervisorParallelYamlMockRun(t *testing.T) {
	orch, rec, sessMgr, sessStore := newPipelineIntegrationOrchestrator(t)
	ctx := context.Background()

	spec, err := orch.LoadTopology(ctx, bundledTopologyPath(t, "supervisor-parallel.yaml"))
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
	if len(rec.order) != 3 {
		t.Fatalf("dispatch count = %d want 3", len(rec.order))
	}
	if result.FinalOutput == "" {
		t.Fatal("expected final output")
	}
}
