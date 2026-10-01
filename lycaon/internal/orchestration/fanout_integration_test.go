//go:build integration

package orchestration_test

import (
	"context"
	"errors"
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

func fanOutSpec(subtasks ...string) orchestration.TopologySpec {
	return orchestration.TopologySpec{
		Pattern: orchestration.TopologyFanOut,
		Task:    "fan out task",
		FanOut: &orchestration.FanOutSpec{
			ProfileID:   orchestration.ProfilePathExplorer,
			Subtasks:    subtasks,
			MaxWorkers:  5,
			Aggregation: orchestration.AggregationMerge,
		},
	}
}

func newFanOutTestOrchestrator(t *testing.T, rec *fanOutTimestampRecording) (*orchestration.OrchestratorImpl, *delegation.MemoryStore, *session.Manager, *store.Memory) {
	t.Helper()
	delStore := delegation.NewMemoryStore()
	sessStore := store.NewMemory()
	sessMgr := session.NewManager(sessStore, llm.NewMockProvider(nil), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	queue := worker.NewInMemoryQueue(10)
	delMgr := delegation.NewManager(delStore, queue, sessMgr, delegation.AllowGate{})
	if rec == nil {
		rec = &fanOutTimestampRecording{}
	}
	rec.inner = delMgr
	rec.store = delStore
	if rec.order == nil {
		rec.order = make([]string, 0, 8)
	}
	reg := orchestration.NewMemoryAgentRegistryForTest()
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Delegation: rec,
		Store:      delStore,
		Agents:     reg,
	})
	return orch, delStore, sessMgr, sessStore
}

func TestFanOutUnknownProfileFails(t *testing.T) {
	ctx := context.Background()
	orch, _, sessMgr, sessStore := newFanOutTestOrchestrator(t, nil)

	sess, err := sessMgr.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology: orchestration.TopologySpec{
			Pattern: orchestration.TopologyFanOut,
			FanOut: &orchestration.FanOutSpec{
				ProfileID: "missing-agent",
				Subtasks:  []string{"one"},
			},
		},
		Input: map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	if err == nil {
		t.Fatal("expected unknown profile error")
	}
}

func TestFanOutParallelDispatch(t *testing.T) {
	ctx := context.Background()
	rec := &fanOutTimestampRecording{recordingDelegation: recordingDelegation{order: make([]string, 0, 4)}}
	orch, _, sessMgr, sessStore := newFanOutTestOrchestrator(t, rec)

	sess, err := sessMgr.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  fanOutSpec("task a", "task b", "task c"),
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	testutil.FailErr(t, "orch.Run failed", err)
	if len(rec.order) != 3 {
		t.Fatalf("dispatch count = %d want 3", len(rec.order))
	}

	times := make([]time.Time, 0, 3)
	for _, key := range []string{"subtask-0", "subtask-1", "subtask-2"} {
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
				t.Fatalf("subtasks not parallel enough: delta=%v", delta)
			}
		}
	}
}

func TestFanOutReconYamlMockRun(t *testing.T) {
	orch, rec, sessMgr, sessStore := newPipelineIntegrationOrchestrator(t)
	ctx := context.Background()

	spec, err := orch.LoadTopology(ctx, bundledTopologyPath(t, "fan-out-recon.yaml"))
	testutil.FailErr(t, "orch.LoadTopology failed", err)
	if spec.Pattern != orchestration.TopologyFanOut {
		t.Fatalf("pattern = %q", spec.Pattern)
	}

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
		t.Fatal("expected merged final output")
	}
	if result.StageOutputs["subtask-0"] == "" || result.StageOutputs["subtask-2"] == "" {
		t.Fatalf("stage outputs = %v", result.StageOutputs)
	}
}

func TestFanOutLateCancelPreservesCompletedDelegation(t *testing.T) {
	ctx := context.Background()
	orch, store, sessMgr, sessStore := newFanOutTestOrchestrator(t, nil)

	sess, err := sessMgr.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	result, err := orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  fanOutSpec("task a", "task b"),
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	testutil.FailErr(t, "orch.Run failed", err)
	if err := orch.Cancel(ctx, result.RunID, orchestration.TerminationReasonHumanAbort); err != nil {
		testutil.FailErr(t, "orch.Cancel failed", err)
	}
	delegationID, ok := store.DelegationBySessionID(sess.ID)
	if !ok {
		t.Fatal("missing delegation")
	}
	del, err := store.Get(ctx, delegationID)
	testutil.FailErr(t, "store.Get failed", err)
	if del.Status != api.DelegationStatusDone {
		t.Fatalf("delegation status = %q want done", del.Status)
	}
	status, err := orch.Status(ctx, result.RunID)
	testutil.FailErr(t, "read completed run status", err)
	if status.Active || status.Phase != "done" {
		t.Fatalf("late cancellation changed completed run: %+v", status)
	}
}

func TestFanOutCancelAbortsActiveDelegation(t *testing.T) {
	ctx := t.Context()
	dispatched := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	rec := &fanOutTimestampRecording{recordingDelegation: recordingDelegation{dispatched: dispatched, holdOutcomes: release}}
	orch, delStore, sessMgr, sessStore := newFanOutTestOrchestrator(t, rec)
	sess, err := sessMgr.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "create orchestration session", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)
	finished := make(chan error, 1)
	go func() {
		_, runErr := orch.Run(ctx, orchestration.RunRequest{
			SessionID: sess.ID, Topology: fanOutSpec("task a", "task b"),
			Input: map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
		})
		finished <- runErr
	}()
	for range 2 {
		select {
		case <-dispatched:
		case runErr := <-finished:
			t.Fatalf("fan-out ended before both workers were dispatched: %v", runErr)
		case <-ctx.Done():
			t.Fatal("test context ended before workers were dispatched")
		}
	}
	delegationID, ok := delStore.DelegationBySessionID(sess.ID)
	if !ok {
		t.Fatal("missing active delegation")
	}
	runID := orchestration.RunIDForDelegationForTest(orch, delegationID)
	status, err := orch.Status(ctx, runID)
	testutil.FailErr(t, "read active run", err)
	if !status.Active {
		t.Fatalf("run is not active before cancellation: %+v", status)
	}
	testutil.FailErr(t, "cancel active fan-out", orch.Cancel(ctx, runID, orchestration.TerminationReasonHumanAbort))
	unblock()
	runErr := <-finished
	var failure *orchestration.RunFailure
	if !errors.As(runErr, &failure) || failure.Code != orchestration.RunFailureCodeStageFailed {
		t.Fatalf("canceled fan-out returned %v, want stage failure", runErr)
	}
	del, err := delStore.Get(ctx, delegationID)
	testutil.FailErr(t, "read aborted delegation", err)
	if del.Status != api.DelegationStatusAborted {
		t.Fatalf("delegation status = %q want aborted", del.Status)
	}
	legs, err := delStore.ListLegs(ctx, delegationID)
	testutil.FailErr(t, "read canceled legs", err)
	for _, leg := range legs {
		if leg.Status != api.LegStatusCanceled {
			t.Fatalf("leg %s status = %q want canceled", leg.ID, leg.Status)
		}
	}
	status, err = orch.Status(ctx, runID)
	testutil.FailErr(t, "read canceled run", err)
	if status.Active || status.Phase != string(orchestration.TerminationReasonHumanAbort) {
		t.Fatalf("canceled run lost its terminal reason: %+v", status)
	}
}

func TestFanOutCancelAfterSettlementBeforeReturnPreservesDone(t *testing.T) {
	ctx := t.Context()
	settled := make(chan struct{}, 2)
	release := make(chan struct{})
	defer close(release)
	rec := &fanOutTimestampRecording{recordingDelegation: recordingDelegation{settled: settled, holdReturns: release}}
	orch, delStore, sessMgr, sessStore := newFanOutTestOrchestrator(t, rec)
	sess, err := sessMgr.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "create session", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)
	finished := make(chan error, 1)
	go func() {
		_, runErr := orch.Run(ctx, orchestration.RunRequest{SessionID: sess.ID, Topology: fanOutSpec("a", "b"), Input: map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID}})
		finished <- runErr
	}()
	for range 2 {
		select {
		case <-settled:
		case err := <-finished:
			t.Fatalf("run ended before settlement: %v", err)
		case <-ctx.Done():
			t.Fatal("context ended before settlement")
		}
	}
	delegationID, _ := delStore.DelegationBySessionID(sess.ID)
	runID := orchestration.RunIDForDelegationForTest(orch, delegationID)
	testutil.FailErr(t, "cancel settled run", orch.Cancel(ctx, runID, orchestration.TerminationReasonHumanAbort))
	status, err := orch.Status(ctx, runID)
	testutil.FailErr(t, "read settled status", err)
	if status.Active || status.Phase != "done" {
		t.Fatalf("settled run changed: %+v", status)
	}
	// Release each dispatch without closing the cleanup-owned channel twice.
	release <- struct{}{}
	release <- struct{}{}
	testutil.FailErr(t, "complete settled run", <-finished)
}

type blockedFanOutCreate struct {
	*delegation.MemoryStore
	entered chan struct{}
	release chan struct{}
}

func (s *blockedFanOutCreate) Create(ctx context.Context, d api.Delegation, sessionID string, legs []api.Leg) (*api.Delegation, error) {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.MemoryStore.Create(ctx, d, sessionID, legs)
}

func TestFanOutCancellationDuringSetupAbortsBeforeDispatch(t *testing.T) {
	ctx := t.Context()
	rec := &fanOutTimestampRecording{}
	_, delStore, sessMgr, sessStore := newFanOutTestOrchestrator(t, rec)
	blocked := &blockedFanOutCreate{MemoryStore: delStore, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(blocked.release) }) }
	defer release()
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{Delegation: rec, Store: blocked, Agents: orchestration.NewMemoryAgentRegistryForTest()})
	sess, err := sessMgr.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "create session", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)
	finished := make(chan error, 1)
	go func() {
		_, runErr := orch.Run(ctx, orchestration.RunRequest{SessionID: sess.ID, Topology: fanOutSpec("a", "b"), Input: map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID}})
		finished <- runErr
	}()
	select {
	case <-blocked.entered:
	case err := <-finished:
		t.Fatalf("run ended before setup: %v", err)
	case <-ctx.Done():
		t.Fatal("context ended before setup")
	}
	runID := orchestration.RunIDForDelegationForTest(orch, "")
	testutil.FailErr(t, "cancel setup", orch.Cancel(ctx, runID, orchestration.TerminationReasonHumanAbort))
	release()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("setup returned %v, want canceled", err)
	}
	id, _ := delStore.DelegationBySessionID(sess.ID)
	d, err := delStore.Get(ctx, id)
	testutil.FailErr(t, "read aborted setup", err)
	if d.Status != api.DelegationStatusAborted || len(rec.order) != 0 {
		t.Fatalf("setup cancellation: status=%q dispatched=%v", d.Status, rec.order)
	}
}

type fanOutTimestampRecording struct {
	recordingDelegation
	mu         sync.Mutex
	timestamps map[string]time.Time
}

func (r *fanOutTimestampRecording) DispatchLeg(ctx context.Context, delegationID, legID, sourceToolCallID string) (*api.Leg, error) {
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
