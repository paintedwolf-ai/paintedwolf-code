package loopwake

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWakeQueuedAfterExecutionReleaseStartsWithoutAnotherEvent(t *testing.T) {
	engine := NewLoopEngine()
	const id = "finishing-turn"
	var prompts atomic.Int32
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: id, Status: api.SessionStatusBusy}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "work", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	finish := engine.BeginPromptExecution(t.Context(), id)
	defer finish()
	deps.QueueInform = func(context.Context, string, anchor.ID, anchor.Envelope) {
		// evaluate saw execution active, but the turn releases before the wake is queued.
		finish()
	}
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)
	engine.Nudge(t.Context(), id, anchor.WorkerTaskFinished, anchor.WorkerTaskFinished, "", anchor.Envelope{})
	testutil.WaitFor(t, 2*time.Second, func() bool { return prompts.Load() == 1 })
	engine.asyncTurns.Wait()
	if engine.HasPendingLoopWakes(id) || prompts.Load() != 1 {
		t.Fatalf("wake settlement: pending=%v prompts=%d", engine.HasPendingLoopWakes(id), prompts.Load())
	}
}

func TestExecutionReleaseCannotClearANewerOwner(t *testing.T) {
	engine := NewLoopEngine()
	first := engine.BeginPromptExecution(t.Context(), "owner")
	second := engine.BeginPromptExecution(t.Context(), "owner")
	defer second()
	first()
	if !engine.PromptExecutionActive("owner") {
		t.Fatal("earlier release cleared the newer execution owner")
	}
	second()
	if engine.PromptExecutionActive("owner") {
		t.Fatal("current owner did not release execution")
	}
}

func TestWorkerCycleTerminalDoesNotOccupyTheWorkerDuringHostPrompt(t *testing.T) {
	engine := NewLoopEngine()
	const id = "delivered-worker"
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: id, Status: api.SessionStatusBusy}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "work", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	release := make(chan struct{})
	started := make(chan struct{})
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		close(started)
		<-release
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)
	engine.enqueuePending(id, pendingLoopWake{wake: anchor.WorkerTaskFinished, seq: engine.nudgeSeq.Add(1)})
	returned := make(chan struct{})
	go func() {
		engine.OnWorkerCycleTerminal(t.Context(), id, "worker")
		close(returned)
	}()
	t.Cleanup(func() {
		close(release)
		<-returned
		engine.asyncTurns.Wait()
	})
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("worker delivery waited for the coordinator's model turn")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("acknowledged worker did not start its pending host turn")
	}
}
