package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestPostTurnDrainRetiresWakesNudgedBeforeTurnStart(t *testing.T) {
	engine := NewLoopEngine()
	const id = "sess-consumed"
	prompts := 0
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: id}, nil
	}
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts++
		return &promptresult.Result{}, nil
	}
	// Sequence order, not actionability, decides this case.
	deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return true }
	engine.SetDeps(deps)

	// The losing wake was nudged before the winning turn started.
	preTurnSeq := engine.Nudges.nudgeSeq.Add(1)
	engine.Nudges.enqueuePending(id, pendingLoopWake{wake: anchor.LegFinished, seq: preTurnSeq})
	engine.Observations.promptObservedSeq.Store(id, engine.Nudges.nudgeSeq.Add(1))

	engine.Nudges.drainPending(context.Background(), id, true)
	if prompts != 0 {
		t.Fatalf("pre-turn wake ran %d prompt(s); the completed turn already consumed it", prompts)
	}
	if _, ok := engine.Nudges.sessionPendingQueue(id).peek(); ok {
		t.Fatal("consumed wake must leave the pending queue")
	}
}

func TestDrainRetiresWakesConsumedByTheTurnItJustRan(t *testing.T) {
	engine := NewLoopEngine()
	const id = "sess-drain-consumed"
	prompts := 0
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: id}, nil
	}
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts++
		engine.Observations.ObservePrompt(id)(inject.CoordinatorTurnFrame{})
		return &promptresult.Result{}, nil
	}
	deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return true }
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-drain", Status: api.WorkflowRunStatusRunning},
	})
	engine.SetDeps(deps)

	// One prompt observes both queued wakes.
	engine.Nudges.enqueuePending(id, pendingLoopWake{wake: anchor.PhaseAdvanced, seq: engine.Nudges.nudgeSeq.Add(1)})
	engine.Nudges.enqueuePending(id, pendingLoopWake{wake: anchor.WorkerTaskFinished, seq: engine.Nudges.nudgeSeq.Add(1)})

	engine.Nudges.drainPending(context.Background(), id, false)
	if prompts != 1 {
		t.Fatalf("drain ran %d prompt(s); the second wake was assembled into the first turn", prompts)
	}
	if _, ok := engine.Nudges.sessionPendingQueue(id).peek(); ok {
		t.Fatal("consumed wake must leave the pending queue")
	}
}

func TestPostTurnDrainKeepsWakesNudgedDuringTurn(t *testing.T) {
	engine := NewLoopEngine()
	const id = "sess-fresh"
	prompts := 0
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: id}, nil
	}
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts++
		return &promptresult.Result{}, nil
	}
	deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return true }
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-fresh", Status: api.WorkflowRunStatusRunning},
	})
	engine.SetDeps(deps)

	engine.Observations.promptObservedSeq.Store(id, engine.Nudges.nudgeSeq.Add(1))
	// Nudged after the turn started — its facts postdate prompt assembly.
	engine.Nudges.enqueuePending(id, pendingLoopWake{wake: anchor.LegFinished, seq: engine.Nudges.nudgeSeq.Add(1)})

	engine.Nudges.drainPending(context.Background(), id, true)
	if prompts != 1 {
		t.Fatalf("during-turn wake ran %d prompt(s), want 1", prompts)
	}
}

func TestPostTurnDrainDropsDormantPhaseAdvanceAfterCloseout(t *testing.T) {
	engine := NewLoopEngine()
	const id = "sess-settled-phase"
	prompts := 0
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: id}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-settled", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return false }
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts++
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)

	engine.Observations.promptObservedSeq.Store(id, engine.Nudges.nudgeSeq.Add(1))
	engine.Nudges.enqueuePending(id, pendingLoopWake{wake: anchor.PhaseAdvanced, seq: engine.Nudges.nudgeSeq.Add(1)})

	engine.Nudges.drainPending(context.Background(), id, true)
	if prompts != 0 {
		t.Fatalf("dormant phase advance ran %d prompt(s) after closeout, want 0", prompts)
	}
	if _, ok := engine.Nudges.sessionPendingQueue(id).peek(); ok {
		t.Fatal("non-actionable phase advance must leave the pending queue")
	}
}

func TestPostTurnDrainKeepsActionablePhaseAdvance(t *testing.T) {
	engine := NewLoopEngine()
	const id = "sess-actionable-phase"
	prompts := 0
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: id}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-actionable", Status: api.WorkflowRunStatusRunning, CurrentPhase: "review"},
	})
	deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return true }
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts++
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)

	engine.Observations.promptObservedSeq.Store(id, engine.Nudges.nudgeSeq.Add(1))
	engine.Nudges.enqueuePending(id, pendingLoopWake{wake: anchor.PhaseAdvanced, seq: engine.Nudges.nudgeSeq.Add(1)})

	engine.Nudges.drainPending(context.Background(), id, true)
	if prompts != 1 {
		t.Fatalf("actionable phase advance ran %d prompt(s), want 1", prompts)
	}
}

func TestActivePromptPhaseAdvanceDefersActionabilityUntilDrain(t *testing.T) {
	engine := NewLoopEngine()
	const id = "sess-busy-phase"
	prompts := 0
	informs := 0
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: id, Status: api.SessionStatusBusy}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-busy", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return false }
	deps.QueueInform = func(context.Context, string, anchor.ID, anchor.Envelope) { informs++ }
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts++
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)
	finishExecution := engine.Admission.BeginPromptExecution(t.Context(), id)

	engine.Nudges.Nudge(context.Background(), id, anchor.PhaseAdvanced, anchor.PhaseAdvanced, "", anchor.Envelope{})
	if wake, ok := engine.Nudges.Pending(id); !ok || wake != anchor.PhaseAdvanced {
		t.Fatalf("pending wake = %q, %v want %q, true", wake, ok, anchor.PhaseAdvanced)
	}
	if informs != 1 {
		t.Fatalf("phase informs = %d want 1 before busy deferral", informs)
	}

	finishExecution()
	engine.Nudges.drainPending(context.Background(), id, false)
	if prompts != 0 {
		t.Fatalf("settled phase wake ran %d prompt(s) after drain, want 0", prompts)
	}
	if _, ok := engine.Nudges.Pending(id); ok {
		t.Fatal("non-actionable drained wake must be retired")
	}
}
