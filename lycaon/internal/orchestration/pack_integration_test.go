//go:build integration

package orchestration_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/orchestration/orchestrationtest"
	"github.com/lycaon/lycaon/internal/tools/toolstest"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func packTopologySpec(count int, merge orchestration.MergeStrategy) orchestration.TopologySpec {
	return orchestration.TopologySpec{
		Pattern: orchestration.TopologyPack,
		Task:    "homogeneous probe task",
		Pack: &orchestration.PackSpec{
			ProfileID:     orchestration.ProfilePathExplorer,
			Count:         count,
			MergeStrategy: merge,
		},
	}
}

func newPackTestOrchestrator(t *testing.T, rec *recordingWorkers) (*orchestration.OrchestratorImpl, *orchestration.MemoryTopologyWorkers, *session.Host, *store.Memory) {
	t.Helper()
	mem := orchestration.NewMemoryTopologyWorkers()
	sessStore := store.NewMemory()
	sessMgr := session.NewHost(sessStore, session.Models{Client: llm.NewMockProvider(nil), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, toolstest.NewRegistry())
	if rec == nil {
		rec = &recordingWorkers{}
	}
	rec.TopologyWorkers = mem
	if rec.order == nil {
		rec.order = make([]string, 0, 8)
	}
	reg := orchestrationtest.Registry(t)
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Workers: rec,
		Agents:  reg,
	})
	return orch, mem, sessMgr, sessStore
}

func TestPackParallelProbeDispatch(t *testing.T) {
	ctx := t.Context()
	rec := &recordingWorkers{
		order:      make([]string, 0, 4),
		barrierSet: map[string]bool{"probe-0": true, "probe-1": true, "probe-2": true},
	}
	orch, _, sessMgr, sessStore := newPackTestOrchestrator(t, rec)

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  packTopologySpec(3, orchestration.MergeFirstValid),
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	testutil.FailErr(t, "orch.Run failed", err)
	if len(rec.order) != 3 || rec.barrierSeen != 3 {
		t.Fatalf("dispatch count = %d barrier = %d want 3", len(rec.order), rec.barrierSeen)
	}
}

func TestPackFirstValidSkipsFailedProbe(t *testing.T) {
	ctx := context.Background()
	rec := &recordingWorkers{failStage: "probe-0"}
	orch, _, sessMgr, sessStore := newPackTestOrchestrator(t, rec)

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	result, err := orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  packTopologySpec(3, orchestration.MergeFirstValid),
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	testutil.FailErr(t, "orch.Run failed", err)
	if result.FinalOutput != "probe-1 done" {
		t.Fatalf("final output = %q want probe-1 done", result.FinalOutput)
	}
}

func TestPackProbeYamlMockRun(t *testing.T) {
	orch, rec, sessMgr, sessStore := newPipelineIntegrationOrchestrator(t)
	ctx := context.Background()

	spec, err := orch.LoadTopology(ctx, bundledTopologyPath(t, "pack-probe.yaml"))
	testutil.FailErr(t, "orch.LoadTopology failed", err)
	if spec.Pattern != orchestration.TopologyPack {
		t.Fatalf("pattern = %q", spec.Pattern)
	}

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
		t.Fatal("expected merged final output")
	}
	if result.StageOutputs["probe-0"] == "" {
		t.Fatalf("stage outputs = %v", result.StageOutputs)
	}
}

func TestPackAllFailReturnsError(t *testing.T) {
	ctx := context.Background()
	rec := &recordingWorkers{failStages: map[string]bool{"probe-0": true, "probe-1": true}}
	orch, _, sessMgr, sessStore := newPackTestOrchestrator(t, rec)

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  packTopologySpec(2, orchestration.MergeFirstValid),
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	if err == nil {
		t.Fatal("expected error when all probes fail")
	}
}
