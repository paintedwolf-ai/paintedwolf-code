package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoopDefersPhaseAdvancedWhileWorkersInFlight(t *testing.T) {
	workspaceDir := t.TempDir()
	engine := NewLoopEngine()
	var prompts atomic.Int32
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: workspaceDir}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	deps.WorkerCycleIdle = func(context.Context, *api.Session, string) (bool, error) {
		return false, nil
	}
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)
	engine.Nudge(context.Background(), "s1", anchor.PhaseAdvanced, anchor.PhaseAdvanced, "", anchor.Envelope{})
	if prompts.Load() != 0 {
		t.Fatalf("prompts = %d want 0 while workers in flight", prompts.Load())
	}
	engine.OnWorkerCycleTerminal(context.Background(), "s1", "")
	if prompts.Load() != 0 {
		t.Fatal("expected deferred flush to schedule prompt after cycle idle")
	}
	deps = loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: workspaceDir}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)
	engine.OnWorkerCycleTerminal(context.Background(), "s1", "")
	testutil.WaitFor(t, 2*time.Second, func() bool { return prompts.Load() >= 1 })
}

func TestWorkerOutcomeAcknowledgementDrainsDeferredPhaseWake(t *testing.T) {
	ctx := t.Context()
	engine := NewLoopEngine()
	var acknowledged atomic.Bool
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusBusy}, nil
	}
	deps.WorkerCycleIdle = func(_ context.Context, _ *api.Session, completingJobID string) (bool, error) {
		return acknowledged.Load() || completingJobID == "job-a", nil
	}
	deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return false }
	engine.SetDeps(deps)

	engine.Nudge(ctx, "s1", anchor.PhaseAdvanced, anchor.PhaseAdvanced, "", anchor.Envelope{})
	if !engine.HasPendingLoopWakes("s1") {
		t.Fatal("phase wake must defer while the worker outcome is pending")
	}
	engine.OnWorkerCycleTerminal(ctx, "s1", "job-a")
	if !engine.HasPendingLoopWakes("s1") {
		t.Fatal("outcome must remain pending until delivery is acknowledged")
	}
	acknowledged.Store(true)
	engine.DrainPending(ctx, "s1")
	if engine.HasPendingLoopWakes("s1") {
		t.Fatal("acknowledged worker outcome stranded a deferred phase wake")
	}
}

func TestWorkerAcknowledgementRunsAnUnconsumedTerminalWake(t *testing.T) {
	engine := NewLoopEngine()
	deps := loopDepsForTest()
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	workspace := t.TempDir()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: workspace}, nil
	}
	deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return false }
	deps.WorkerCycleIdle = func(context.Context, *api.Session, string) (bool, error) { return true, nil }
	var prompts atomic.Int32
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)
	engine.enqueuePending("s1", pendingLoopWake{wake: anchor.WorkerTaskFinished, seq: 1})
	engine.OnWorkerCycleTerminal(t.Context(), "s1", "job")
	testutil.WaitFor(t, 2*time.Second, func() bool { return prompts.Load() == 1 })
	if engine.HasPendingLoopWakes("s1") {
		t.Fatal("delivered terminal wake remained queued")
	}
}

func TestLoopNudgeAfterWorkerJobTerminalRunsPerJobWake(t *testing.T) {
	workspaceDir := t.TempDir()
	engine := NewLoopEngine()
	var prompts atomic.Int32
	excludeJob := "job-a"
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: workspaceDir}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	deps.WorkerCycleIdle = func(_ context.Context, _ *api.Session, completingJobID string) (bool, error) {
		if strings.TrimSpace(completingJobID) == excludeJob {
			return true, nil
		}
		return false, nil
	}
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)
	engine.NudgeAfterWorkerJobTerminal(
		context.Background(),
		"s1",
		excludeJob,
		anchor.WorkerTaskFinished,
		anchor.WorkerTaskFinished,
		"",
		anchor.Envelope{},
	)
	testutil.WaitFor(t, 2*time.Second, func() bool { return prompts.Load() >= 1 })
	if prompts.Load() != 1 {
		t.Fatalf("prompts = %d want per-job wake while siblings in flight", prompts.Load())
	}
}

func TestWorkerCompletionBreaksSleep(t *testing.T) {
	workspaceDir := t.TempDir()
	engine := NewLoopEngine()
	var prompts atomic.Int32
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: workspaceDir}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)
	engine.EnterSleep(context.Background(), "s1", time.Now().UTC().Add(30*time.Minute), "scouts running", DefaultCoordinatorWaitTriggers(false), nil, SleepMoverHost)
	if !engine.IsSleeping("s1") {
		t.Fatal("expected sleeping after wait")
	}
	engine.NudgeAfterWorkerJobTerminal(context.Background(), "s1", "job-1", anchor.WorkerTaskFinished, anchor.WorkerTaskFinished, "", anchor.Envelope{})
	testutil.WaitFor(t, 2*time.Second, func() bool { return prompts.Load() >= 1 })
	if engine.IsSleeping("s1") {
		t.Fatal("worker completion should break sleep")
	}
}

func TestWorkerTaskFinishedKickDedupPerJob(t *testing.T) {
	engine := NewLoopEngine()
	var kicks atomic.Int32
	projectDir := t.TempDir()
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: projectDir}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		return &promptresult.Result{}, nil
	}
	deps.QueueInform = func(_ context.Context, sessionID string, inform anchor.ID, env anchor.Envelope) { kicks.Add(1) }
	engine.SetDeps(deps)
	engine.NudgeAfterWorkerJobTerminal(context.Background(), "s1", "job-1", anchor.WorkerTaskFinished, anchor.WorkerTaskFinished, "", anchor.Envelope{})
	engine.NudgeAfterWorkerJobTerminal(context.Background(), "s1", "job-2", anchor.WorkerTaskFinished, anchor.WorkerTaskFinished, "", anchor.Envelope{})
	testutil.WaitFor(t, 2*time.Second, func() bool { return kicks.Load() >= 2 })
	if kicks.Load() != 2 {
		t.Fatalf("kicks = %d want 2 (per-job dedup)", kicks.Load())
	}
}

func TestPhaseReenterFinishKickCoversTheJobFinishKick(t *testing.T) {
	engine := NewLoopEngine()
	projectDir := t.TempDir()
	kickEngine := &kick.KickEngine{}
	kickEngine.QueueDeferred("s1", anchor.InformRender(anchor.WorkerTaskFinished))
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: projectDir}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		return &promptresult.Result{}, nil
	}
	deps.QueueInform = func(_ context.Context, sessionID string, inform anchor.ID, env anchor.Envelope) {
		kickEngine.QueueDeferred(sessionID, anchor.InformRender(inform), env.KickOptions()...)
	}
	engine.SetDeps(deps)
	engine.NudgeAfterWorkerJobTerminal(context.Background(), "s1", "job-1", anchor.WorkerTaskFinished, anchor.WorkerTaskFinished, "", anchor.Envelope{})
	wantID := anchor.InformRender(anchor.WorkerTaskFinished)
	if ids := kickEngine.PendingKickIDsUnless("s1", nil); len(ids) != 1 || ids[0] != wantID {
		t.Fatalf("pending kicks = %v want the one %q the phase re-enter hook queued", ids, wantID)
	}
}

func TestPromptExecutionQueuesMultipleNudgesWithoutLoss(t *testing.T) {
	workspaceDir := t.TempDir()
	engine := NewLoopEngine()
	var prompts atomic.Int32
	sess := &api.Session{ID: "s1", Status: api.SessionStatusBusy, WorkspacePath: workspaceDir}
	kickEngine := &kick.KickEngine{}
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) { return sess, nil }
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	// This stub leaves guidance queued, so each wake requires a turn.
	deps.QueueInform = func(_ context.Context, sessionID string, inform anchor.ID, env anchor.Envelope) {
		kickEngine.QueueDeferred(sessionID, anchor.InformRender(inform), env.KickOptions()...)
	}
	deps.HasQueuedKick = kickEngine.HasQueuedKick
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)
	finishExecution := engine.BeginPromptExecution(t.Context(), "s1")
	engine.Nudge(context.Background(), "s1", anchor.WorkerTaskFinished, anchor.WorkerTaskFinished, "", anchor.Envelope{})
	engine.Nudge(context.Background(), "s1", anchor.LegFinished, anchor.LegFinished, "leg-1", anchor.Envelope{})
	finishExecution()
	engine.OnWorkerCycleTerminal(context.Background(), "s1", "")
	testutil.WaitFor(t, 2*time.Second, func() bool { return prompts.Load() >= 2 })
	if prompts.Load() != 2 {
		t.Fatalf("prompts = %d want 2 distinct deferred nudges", prompts.Load())
	}
}

func TestOnWorkerCycleTerminalFlushesPromptExecutionDeferral(t *testing.T) {
	workspaceDir := t.TempDir()
	engine := NewLoopEngine()
	var prompts atomic.Int32
	sess := &api.Session{ID: "s1", Status: api.SessionStatusBusy, WorkspacePath: workspaceDir}
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) { return sess, nil }
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)
	finishExecution := engine.BeginPromptExecution(t.Context(), "s1")
	engine.Nudge(context.Background(), "s1", anchor.LegFinished, anchor.LegFinished, "leg-1", anchor.Envelope{})
	if _, ok := engine.PendingForTest("s1"); !ok {
		t.Fatal("expected deferral while prompt execution is active")
	}
	if prompts.Load() != 0 {
		t.Fatal("expected no prompt while prompt execution is active")
	}
	finishExecution()
	engine.OnWorkerCycleTerminal(context.Background(), "s1", "")
	testutil.WaitFor(t, 2*time.Second, func() bool { return prompts.Load() >= 1 })
	if _, ok := engine.PendingForTest("s1"); ok {
		t.Fatal("deferral should be cleared after terminal flush")
	}
}
