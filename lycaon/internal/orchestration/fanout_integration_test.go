//go:build integration

package orchestration_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/tools/toolstest"
	"sync"
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

func newFanOutTestOrchestrator(t *testing.T, rec *recordingWorkers) (*orchestration.OrchestratorImpl, *orchestration.MemoryTopologyWorkers, *session.Host, *store.Memory) {
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
	reg := orchestration.NewMemoryAgentRegistryForTest()
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Workers: rec,
		Agents:  reg,
	})
	return orch, mem, sessMgr, sessStore
}

func TestFanOutUnknownProfileFails(t *testing.T) {
	ctx := context.Background()
	orch, _, sessMgr, sessStore := newFanOutTestOrchestrator(t, nil)

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
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
	ctx := t.Context()
	rec := &recordingWorkers{
		order:      make([]string, 0, 4),
		barrierSet: map[string]bool{"subtask-0": true, "subtask-1": true, "subtask-2": true},
	}
	orch, _, sessMgr, sessStore := newFanOutTestOrchestrator(t, rec)

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  fanOutSpec("task a", "task b", "task c"),
		Input:     map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	testutil.FailErr(t, "orch.Run failed", err)
	if len(rec.order) != 3 || rec.barrierSeen != 3 {
		t.Fatalf("dispatch count = %d barrier = %d want 3", len(rec.order), rec.barrierSeen)
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
	if result.StageOutputs["subtask-0"] == "" || result.StageOutputs["subtask-2"] == "" {
		t.Fatalf("stage outputs = %v", result.StageOutputs)
	}
}

func TestFanOutLateCancelPreservesCompletedBatch(t *testing.T) {
	ctx := context.Background()
	orch, mem, sessMgr, sessStore := newFanOutTestOrchestrator(t, nil)

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
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
	batchID, ok, err := mem.BatchBySession(ctx, sess.ID)
	testutil.FailErr(t, "BatchBySession failed", err)
	if !ok {
		t.Fatal("missing topology batch")
	}
	b, err := mem.GetBatch(ctx, batchID)
	testutil.FailErr(t, "mem.GetBatch failed", err)
	if b.Status != orchestration.TopologyBatchDone {
		t.Fatalf("batch status = %q want done", b.Status)
	}
	status, err := orch.Status(ctx, result.RunID)
	testutil.FailErr(t, "read completed run status", err)
	if status.Active || status.Phase != "done" {
		t.Fatalf("late cancellation changed completed run: %+v", status)
	}
}

func TestFanOutCancelAbortsActiveBatch(t *testing.T) {
	ctx := t.Context()
	dispatched := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	rec := &recordingWorkers{dispatched: dispatched, holdOutcomes: release}
	orch, mem, sessMgr, sessStore := newFanOutTestOrchestrator(t, rec)
	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
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
	batchID, ok, err := mem.BatchBySession(ctx, sess.ID)
	testutil.FailErr(t, "BatchBySession failed", err)
	if !ok {
		t.Fatal("missing active topology batch")
	}
	runID := orchestration.RunIDForBatchForTest(orch, batchID)
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
	b, err := mem.GetBatch(ctx, batchID)
	testutil.FailErr(t, "read aborted batch", err)
	if b.Status != orchestration.TopologyBatchAborted {
		t.Fatalf("batch status = %q want aborted", b.Status)
	}
	units, err := mem.ListUnits(ctx, batchID)
	testutil.FailErr(t, "read canceled units", err)
	for _, u := range units {
		if u.Status != api.LegStatusCanceled {
			t.Fatalf("unit %s status = %q want canceled", u.ID, u.Status)
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
	rec := &recordingWorkers{settled: settled, holdReturns: release}
	orch, mem, sessMgr, sessStore := newFanOutTestOrchestrator(t, rec)
	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
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
	batchID, _, _ := mem.BatchBySession(ctx, sess.ID)
	runID := orchestration.RunIDForBatchForTest(orch, batchID)
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
	*recordingWorkers
	entered chan struct{}
	release chan struct{}
}

func (s *blockedFanOutCreate) CreateBatch(ctx context.Context, batch orchestration.TopologyBatch, units []orchestration.TopologyUnit) (*orchestration.TopologyBatch, []orchestration.TopologyUnit, error) {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	return s.recordingWorkers.CreateBatch(ctx, batch, units)
}

func TestFanOutCancellationDuringSetupAbortsBeforeDispatch(t *testing.T) {
	ctx := t.Context()
	mem := orchestration.NewMemoryTopologyWorkers()
	rec := &recordingWorkers{TopologyWorkers: mem, order: make([]string, 0, 4)}
	_, _, sessMgr, sessStore := newFanOutTestOrchestrator(t, nil)
	blocked := &blockedFanOutCreate{recordingWorkers: rec, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(blocked.release) }) }
	defer release()
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{Workers: blocked, Agents: orchestration.NewMemoryAgentRegistryForTest()})
	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
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
	runID := orchestration.RunIDForBatchForTest(orch, "")
	testutil.FailErr(t, "cancel setup", orch.Cancel(ctx, runID, orchestration.TerminationReasonHumanAbort))
	release()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("setup returned %v, want canceled", err)
	}
	id, _, _ := mem.BatchBySession(ctx, sess.ID)
	b, err := mem.GetBatch(ctx, id)
	testutil.FailErr(t, "read aborted setup", err)
	if b.Status != orchestration.TopologyBatchAborted || len(rec.order) != 0 {
		t.Fatalf("setup cancellation: status=%q dispatched=%v", b.Status, rec.order)
	}
}
