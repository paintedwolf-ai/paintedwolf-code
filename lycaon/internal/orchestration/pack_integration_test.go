//go:build integration

package orchestration_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/delegation"
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

func newPackTestOrchestrator(t *testing.T, del orchestration.PipelineDelegation) (*orchestration.OrchestratorImpl, *delegation.MemoryStore, *session.Host, *store.Memory) {
	t.Helper()
	delStore := delegation.NewMemoryStore()
	sessStore := store.NewMemory()
	sessMgr := session.NewHost(sessStore, session.Models{Client: llm.NewMockProvider(nil), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	queue := worker.NewInMemoryQueue(10)
	delMgr := delegation.NewManager(delStore, queue, sessMgr, delegation.AllowGate{})
	if del == nil {
		del = &recordingDelegation{inner: delMgr, store: delStore, order: make([]string, 0, 8)}
	} else if rec, ok := del.(*packTimestampRecording); ok {
		rec.inner = delMgr
		rec.store = delStore
		if rec.order == nil {
			rec.order = make([]string, 0, 8)
		}
	}
	reg := orchestration.NewMemoryAgentRegistryForTest()
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Delegation: del,
		Store:      delStore,
		Agents:     reg,
	})
	return orch, delStore, sessMgr, sessStore
}

func newPackTimestampOrchestrator(t *testing.T) (*orchestration.OrchestratorImpl, *packTimestampRecording, *session.Host, *store.Memory) {
	t.Helper()
	rec := &packTimestampRecording{recordingDelegation: recordingDelegation{order: make([]string, 0, 4)}}
	orch, _, sessMgr, sessStore := newPackTestOrchestrator(t, rec)
	return orch, rec, sessMgr, sessStore
}

func TestPackParallelProbeDispatch(t *testing.T) {
	ctx := context.Background()
	orch, rec, sessMgr, sessStore := newPackTimestampOrchestrator(t)

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  packTopologySpec(3, orchestration.MergeFirstValid),
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	testutil.FailErr(t, "orch.Run failed", err)
	if len(rec.order) != 3 {
		t.Fatalf("dispatch count = %d want 3", len(rec.order))
	}

	times := make([]time.Time, 0, 3)
	for _, key := range []string{"probe-0", "probe-1", "probe-2"} {
		ts, ok := rec.timestamps[key]
		if !ok {
			t.Fatalf("missing timestamp for %q", key)
		}
		times = append(times, ts)
	}
	for i := 0; i < len(times); i++ {
		for j := i + 1; j < len(times); j++ {
			delta := times[i].Sub(times[j])
			if delta < 0 {
				delta = -delta
			}
			if delta > 100*time.Millisecond {
				t.Fatalf("probes not parallel enough: delta=%v", delta)
			}
		}
	}
}

func TestPackFirstValidSkipsFailedProbe(t *testing.T) {
	ctx := context.Background()
	delStore := delegation.NewMemoryStore()
	sessStore := store.NewMemory()
	sessMgr := session.NewHost(sessStore, session.Models{Client: llm.NewMockProvider(nil), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	queue := worker.NewInMemoryQueue(10)
	delMgr := delegation.NewManager(delStore, queue, sessMgr, delegation.AllowGate{})
	failDel := &failProbeDelegation{
		recordingDelegation: recordingDelegation{inner: delMgr, store: delStore},
		failProbe:           "probe-0",
	}
	reg := orchestration.NewMemoryAgentRegistryForTest()
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Delegation: failDel,
		Store:      delStore,
		Agents:     reg,
	})

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
	delStore := delegation.NewMemoryStore()
	sessStore := store.NewMemory()
	sessMgr := session.NewHost(sessStore, session.Models{Client: llm.NewMockProvider(nil), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	queue := worker.NewInMemoryQueue(10)
	delMgr := delegation.NewManager(delStore, queue, sessMgr, delegation.AllowGate{})
	failDel := &failProbeDelegation{
		recordingDelegation: recordingDelegation{inner: delMgr, store: delStore},
		failProbe:           "probe-0",
	}
	reg := orchestration.NewMemoryAgentRegistryForTest()
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Delegation: &failAllPackDelegation{inner: failDel, store: delStore},
		Store:      delStore,
		Agents:     reg,
	})

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

type failProbeDelegation struct {
	recordingDelegation
	failProbe string
}

func (f *failProbeDelegation) DispatchLeg(ctx context.Context, delegationID, legID, sourceToolCallID string) (*api.Leg, error) {
	leg, err := f.store.GetLeg(ctx, delegationID, legID)
	if err != nil {
		return nil, err
	}
	if leg.Title == f.failProbe {
		now := time.Now().UTC()
		leg.Status = api.LegStatusFailed
		leg.StartedAt = &now
		if err := f.store.UpdateLeg(ctx, *leg); err != nil {
			return nil, err
		}
		return leg, nil
	}
	return f.recordingDelegation.DispatchLeg(ctx, delegationID, legID, sourceToolCallID)
}

type failAllPackDelegation struct {
	inner orchestration.PipelineDelegation
	store orchestration.PipelineDelegationStore
}

func (f *failAllPackDelegation) DispatchLeg(ctx context.Context, delegationID, legID, sourceToolCallID string) (*api.Leg, error) {
	leg, err := f.store.GetLeg(ctx, delegationID, legID)
	if err != nil {
		return nil, err
	}
	if leg.Title == "probe-1" {
		now := time.Now().UTC()
		leg.Status = api.LegStatusFailed
		leg.StartedAt = &now
		if err := f.store.UpdateLeg(ctx, *leg); err != nil {
			return nil, err
		}
		return leg, nil
	}
	return f.inner.DispatchLeg(ctx, delegationID, legID, sourceToolCallID)
}

func (f *failAllPackDelegation) Abort(ctx context.Context, delegationID, reason string) error {
	return f.inner.Abort(ctx, delegationID, reason)
}

type packTimestampRecording struct {
	recordingDelegation
	mu         sync.Mutex
	timestamps map[string]time.Time
}

func (r *packTimestampRecording) DispatchLeg(ctx context.Context, delegationID, legID, sourceToolCallID string) (*api.Leg, error) {
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
