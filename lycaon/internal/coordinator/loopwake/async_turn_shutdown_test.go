package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/promptresult"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/pkg/api"
)

// budgetRequestEnvelope drives an asynchronous host turn for jobID.
func budgetRequestEnvelope(jobID string) anchor.Envelope {
	return anchor.Envelope{WorkerBudget: &kick.WorkerBudgetFacts{
		JobID: jobID, Used: 16, Max: 20, HostMax: 120,
		Request: &api.WorkerBudgetRequest{
			Rounds: 12, RequestedMax: 32, RemainingWork: []string{"trace the remaining callers"}, ToolLoopsUsed: 16,
		},
	}}
}

func TestWaitForAsyncTurnsForceCancelsAndBlocksUntilExit(t *testing.T) {
	engine := NewLoopEngine()
	ws := t.TempDir()
	started := make(chan struct{})
	sawCancel := make(chan struct{})
	release := make(chan struct{})

	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: ws}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	deps.WorkerCycleIdle = func(context.Context, *api.Session, string) (bool, error) { return false, nil }
	deps.RunPrompt = func(ctx context.Context, _ string) (*promptresult.Result, error) {
		close(started)
		<-ctx.Done()
		close(sawCancel)
		<-release // Simulates cleanup after cancellation.
		return nil, ctx.Err()
	}
	engine.SetDeps(deps)

	engine.NudgeWorkerBudgetRequested(context.Background(), "s1", "job-1", budgetRequestEnvelope("job-1"))

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("async turn never started")
	}

	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		engine.WaitForAsyncTurns(ctx)
	}()

	select {
	case <-sawCancel:
	case <-time.After(2 * time.Second):
		t.Fatal("turn was not force-canceled once the drain deadline passed")
	}

	select {
	case <-waitDone:
		t.Fatal("WaitForAsyncTurns returned while the force-canceled turn was still mid-write")
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForAsyncTurns never returned after the turn actually exited")
	}
}

func TestForgetSessionCancelsAndDrainsInFlightAsyncTurn(t *testing.T) {
	engine := NewLoopEngine()
	ws := t.TempDir()
	started := make(chan struct{})
	sawCancel := make(chan struct{})
	release := make(chan struct{})

	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: ws}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	deps.WorkerCycleIdle = func(context.Context, *api.Session, string) (bool, error) { return false, nil }
	deps.RunPrompt = func(ctx context.Context, _ string) (*promptresult.Result, error) {
		close(started)
		<-ctx.Done()
		close(sawCancel)
		<-release
		return nil, ctx.Err()
	}
	engine.SetDeps(deps)

	engine.NudgeWorkerBudgetRequested(context.Background(), "s1", "job-1", budgetRequestEnvelope("job-1"))

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("async turn never started")
	}

	forgetDone := make(chan struct{})
	go func() {
		defer close(forgetDone)
		engine.ForgetSession(context.Background(), "s1")
	}()

	// Session cleanup waits for the turn to exit.
	select {
	case <-forgetDone:
		t.Fatal("ForgetSession returned while its session's turn was still running")
	case <-time.After(150 * time.Millisecond):
	}

	select {
	case <-sawCancel:
	case <-time.After(2 * time.Second):
		t.Fatal("ForgetSession never canceled the in-flight async turn")
	}

	select {
	case <-forgetDone:
		t.Fatal("ForgetSession returned before the canceled turn actually exited")
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	select {
	case <-forgetDone:
	case <-time.After(2 * time.Second):
		t.Fatal("ForgetSession never returned after the turn exited")
	}

	for name, state := range map[string]*sync.Map{
		"pending":       &engine.pendingQueues,
		"prompt active": &engine.promptActive,
	} {
		if _, ok := state.Load("s1"); ok {
			t.Fatalf("%s state repopulated by the in-flight turn's own cleanup after ForgetSession", name)
		}
	}
}

func TestSpawnAsyncTurnPanicDoesNotLeakRegistryOrWaitGroup(t *testing.T) {
	engine := NewLoopEngine()
	ws := t.TempDir()
	started := make(chan struct{})

	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: ws}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	deps.WorkerCycleIdle = func(context.Context, *api.Session, string) (bool, error) { return false, nil }
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		close(started)
		panic("simulated turn panic")
	}
	engine.SetDeps(deps)

	engine.NudgeWorkerBudgetRequested(context.Background(), "s1", "job-1", budgetRequestEnvelope("job-1"))

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("async turn never started")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	engine.WaitForAsyncTurns(ctx)
	if ctx.Err() != nil {
		t.Fatal("WaitForAsyncTurns timed out — a panicking turn leaked its WaitGroup slot")
	}

	engine.asyncTurnsMu.Lock()
	_, leaked := engine.asyncTurnsBySession["s1"]
	engine.asyncTurnsMu.Unlock()
	if leaked {
		t.Fatal("panicking turn left a stale entry in the per-session async-turn registry")
	}
}
