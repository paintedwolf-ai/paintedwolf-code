package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"sync/atomic"
	"testing"
	"time"
)

func TestDrainPendingDoesNotBurnBudgetWhenPromptActive(t *testing.T) {
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
		time.Sleep(50 * time.Millisecond)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)
	engine.Turns.promptActive.Store("s1", struct{}{})
	for i := 0; i < 8; i++ {
		engine.Nudges.enqueuePending("s1", pendingLoopWake{wake: anchor.WorkerTaskFinished, seq: engine.Nudges.nudgeSeq.Add(1)})
	}
	engine.Nudges.drainPending(context.Background(), "s1", false)
	if prompts.Load() != 0 {
		t.Fatalf("prompts = %d want 0 while prompt active", prompts.Load())
	}
	if !engine.Admission.ConsumeBudget(context.Background(), "s1", "run-1", anchor.WorkerTaskFinished) {
		t.Fatal("expected budget remaining after requeue without run")
	}
}

func TestEnqueuePendingPreservesEventOrder(t *testing.T) {
	engine := NewLoopEngine()
	engine.Nudges.enqueuePending("s1", pendingLoopWake{wake: anchor.WorkerTaskFinished, seq: engine.Nudges.nudgeSeq.Add(1)})
	engine.Nudges.enqueuePending("s1", pendingLoopWake{wake: anchor.WorkerTaskFinished, seq: engine.Nudges.nudgeSeq.Add(1)})
	nudge, ok := engine.Nudges.Pending("s1")
	if !ok || nudge != anchor.WorkerTaskFinished {
		t.Fatalf("pending = %q ok=%v", nudge, ok)
	}
	engine.Nudges.enqueuePending("s1", pendingLoopWake{wake: anchor.LegFinished, seq: engine.Nudges.nudgeSeq.Add(1)})
	nudge, ok = engine.Nudges.Pending("s1")
	if !ok || nudge != anchor.WorkerTaskFinished {
		t.Fatalf("head pending = %q want worker_task_done", nudge)
	}
	for _, want := range []anchor.ID{anchor.WorkerTaskFinished, anchor.WorkerTaskFinished, anchor.LegFinished} {
		got, ok := engine.Nudges.sessionPendingQueue("s1").pop()
		if !ok || got.wake != want {
			t.Fatalf("event order: got %q, want %q", got.wake, want)
		}
	}

}

func TestRunPromptSyncConsumesBudgetOnce(t *testing.T) {
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
	if !engine.Turns.runPromptSync(context.Background(), "s1", pendingLoopWake{wake: anchor.WorkerTaskFinished, seq: engine.Nudges.nudgeSeq.Add(1)}) {
		t.Fatal("expected first runPromptSync to succeed")
	}
	if prompts.Load() != 1 {
		t.Fatalf("prompts = %d want 1", prompts.Load())
	}
}

func TestDrainPendingWorkerWakeAfterCloseoutSkipsPrompt(t *testing.T) {
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
	deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return false }
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)

	// A closeout supersedes a queued worker wake without a live terminal fact.
	engine.Nudges.enqueuePending("s1", pendingLoopWake{wake: anchor.WorkerTaskFinished, seq: engine.Nudges.nudgeSeq.Add(1)})
	engine.Nudges.drainPending(context.Background(), "s1", true)

	if prompts.Load() != 0 {
		t.Fatalf("prompts = %d want 0 after closeout", prompts.Load())
	}
}

// A wake queued under promptActive drains when the host turn releases it.
func TestWakeQueuedDuringHostTurnDrainsOnRelease(t *testing.T) {
	workspaceDir := t.TempDir()
	engine := NewLoopEngine()
	var prompts atomic.Int32
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: workspaceDir}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "review"},
	})
	deps.RunPrompt = func(ctx context.Context, sessionID string) (*promptresult.Result, error) {
		if prompts.Add(1) == 1 {
			// Requeue a transition while the host prompt is active.
			engine.Nudges.enqueuePending(sessionID, pendingLoopWake{wake: anchor.PhaseAdvanced, seq: engine.Nudges.nudgeSeq.Add(1)})
			engine.Nudges.drainPending(ctx, sessionID, false)
		}
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)

	if !engine.Turns.runPromptSync(context.Background(), "s1", pendingLoopWake{wake: anchor.WorkerTaskFinished, seq: engine.Nudges.nudgeSeq.Add(1)}) {
		t.Fatal("outer host turn should run")
	}
	deadline := time.Now().Add(5 * time.Second)
	for prompts.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := prompts.Load(); got != 2 {
		t.Fatalf("prompts = %d want 2 (queued wake must drain after the frame releases)", got)
	}
	if _, ok := engine.Nudges.Pending("s1"); ok {
		t.Fatal("pending queue should be empty after the release re-drain")
	}
}

// Wakes queued during user-turn settlement drain when execution ends.
func TestWakeQueuedDuringPromptExecutionDrainsOnRelease(t *testing.T) {
	workspaceDir := t.TempDir()
	engine := NewLoopEngine()
	var prompts atomic.Int32
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusBusy, WorkspacePath: workspaceDir}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)

	finishExecution := engine.Admission.BeginPromptExecution(t.Context(), "s1")
	engine.Nudges.NudgeAfterWorkerJobTerminal(context.Background(), "s1", "job-1", anchor.WorkerTaskFinished, anchor.WorkerTaskFinished, "", anchor.Envelope{})
	if prompts.Load() != 0 {
		t.Fatal("a wake during execution must wait for the turn")
	}
	finishExecution()
	testutil.WaitFor(t, 2*time.Second, func() bool { return prompts.Load() == 1 })
	if _, ok := engine.Nudges.Pending("s1"); ok {
		t.Fatal("the released turn must drain the parked wake")
	}
}
