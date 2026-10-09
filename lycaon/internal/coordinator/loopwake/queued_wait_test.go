package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/promptresult"
	"sync/atomic"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type queuedWaitFixture struct {
	loop    *LoopEngine
	store   *awaitstore.Store
	deps    LoopDeps
	lease   awaitstore.Lease
	prompts atomic.Int32
}

func newQueuedWaitFixture(t *testing.T) *queuedWaitFixture {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session", testdbseed.DefaultProjectID)
	f := &queuedWaitFixture{loop: NewLoopEngine(), store: &awaitstore.Store{DB: database}, deps: loopDepsForTest()}
	f.loop.SetWaitStore(f.store)
	f.deps.GetSession = func(context.Context, string) (*api.Session, error) { return &api.Session{ID: "session"}, nil }
	f.deps.WorkflowSource = workflowFixturePorts(StubLoopWF{run: &api.WorkflowRun{ID: "run", Revision: 5, CurrentPhase: "work", Status: api.WorkflowRunStatusRunning}})
	f.deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return true }
	f.deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		f.prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	f.loop.SetDeps(f.deps)
	t.Cleanup(func() { f.loop.ForgetSession(context.Background(), "session") })
	return f
}

func (f *queuedWaitFixture) arm(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Hour)
	lease, err := f.store.Arm(t.Context(), awaitstore.Lease{
		SessionID: "session", ProjectID: testdbseed.DefaultProjectID, ToolCallID: "wait-call", ProfileID: "implement", Deadline: deadline,
		Conditions: []awaitstore.Condition{{Kind: "process_done", Handles: []string{"target"}}},
	})
	testutil.FailErr(t, "arm wait", err)
	f.lease = lease
	f.loop.EnterSleep(t.Context(), "session", deadline, "command", []WaitTrigger{WaitTriggerTimer, WaitTriggerProcessDone}, []string{"target"}, SleepMoverHost)
	f.loop.MarkWaitCalled("session")
	if f.loop.OnTurnComplete(t.Context(), "session", false) != UserTurnContinues {
		t.Fatal("wait settled the visible turn")
	}
}

func (f *queuedWaitFixture) drain(t *testing.T, finish func()) {
	t.Helper()
	finish()
	f.loop.WaitForAsyncTurns(testutil.BoundedContext(t, 2*time.Second))
}

func TestQueuedStartupPhaseAlreadyObservedPreservesWait(t *testing.T) {
	f := newQueuedWaitFixture(t)
	finish := f.loop.BeginPromptExecution(t.Context(), "session")
	observe := f.loop.ObservePrompt("session")
	// Startup advances during context assembly, after its sequence boundary.
	f.loop.Nudge(t.Context(), "session", anchor.PhaseAdvanced, "", "", anchor.Envelope{})
	observe(inject.CoordinatorTurnFrame{WorkflowRevision: 5, RunContext: api.CoordinatorRunContext{RunID: "run"}})
	f.arm(t)
	f.drain(t, finish)
	if f.prompts.Load() != 0 || !f.loop.IsSleeping("session") || f.loop.HasPendingLoopWakes("session") {
		t.Fatalf("startup replay: prompts=%d sleeping=%v pending=%v", f.prompts.Load(), f.loop.IsSleeping("session"), f.loop.HasPendingLoopWakes("session"))
	}
	_, active, err := f.store.ForSession(t.Context(), "session")
	testutil.FailErr(t, "read preserved lease", err)
	if !active {
		t.Fatal("observed startup event interrupted the durable wait")
	}
}

func TestQueuedFreshPhaseInterruptsWaitBeforePrompt(t *testing.T) {
	for _, delivered := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed model request", true: "older observed revision"}[delivered], func(t *testing.T) {
			f := newQueuedWaitFixture(t)
			finish := f.loop.BeginPromptExecution(t.Context(), "session")
			observe := f.loop.ObservePrompt("session")
			f.loop.Nudge(t.Context(), "session", anchor.PhaseAdvanced, "", "", anchor.Envelope{})
			if delivered {
				observe(inject.CoordinatorTurnFrame{WorkflowRevision: 4, RunContext: api.CoordinatorRunContext{RunID: "run"}})
			}
			f.arm(t)
			f.deps.RunPrompt = func(ctx context.Context, _ string) (*promptresult.Result, error) {
				_, active, err := f.store.ForSession(ctx, "session")
				if active || f.loop.IsSleeping("session") || f.loop.WaitLeaseOpenForTest("session") {
					t.Error("prompt started with an active wait")
				}
				f.prompts.Add(1)
				return &promptresult.Result{}, err
			}
			f.loop.SetDeps(f.deps)
			f.drain(t, finish)
			if f.prompts.Load() != 1 {
				t.Fatalf("fresh phase prompts=%d, want 1", f.prompts.Load())
			}
		})
	}
}

func TestQueuedProcessEventsRetainHandlesAndSettleOnlyMatchingWait(t *testing.T) {
	f := newQueuedWaitFixture(t)
	finish := f.loop.BeginPromptExecution(t.Context(), "session")
	for _, handle := range []string{"unrelated", "target"} {
		f.loop.Nudge(t.Context(), "session", anchor.ProcessFinished, "", handle, anchor.Envelope{})
	}
	// Observed completions still require wait-result delivery.
	f.loop.ObservePrompt("session")(inject.CoordinatorTurnFrame{})
	f.arm(t)
	f.deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return false }
	f.deps.WorkflowSource = workflowFixturePorts(closedBatchLoopWF(3))
	f.deps.WorkerCycleIdle = func(context.Context, *api.Session, string) (bool, error) { return false, nil }
	var resumes atomic.Int32
	f.deps.RunWaitResume = func(_ context.Context, _ string, delivery WaitDelivery) (*promptresult.Result, error) {
		if delivery.LeaseID != f.lease.ID || delivery.Condition.Kind != "process_done" {
			t.Errorf("wrong delivery: %+v", delivery)
		}
		resumes.Add(1)
		return &promptresult.Result{}, delivery.Admitted()
	}
	f.loop.SetDeps(f.deps)
	f.drain(t, finish)
	if f.prompts.Load() != 0 || resumes.Load() != 1 || f.loop.IsSleeping("session") {
		t.Fatalf("process routing: prompts=%d resumes=%d sleeping=%v", f.prompts.Load(), resumes.Load(), f.loop.IsSleeping("session"))
	}
}

func TestQueuedWakeRetainsDecisionAndBatchIdentity(t *testing.T) {
	f := newQueuedWaitFixture(t)
	finish := f.loop.BeginPromptExecution(t.Context(), "session")
	env := anchor.Envelope{BatchSeq: 3, BatchSeqSet: true}
	env.WithWorkerDecision(api.WorkerDecisionRequest{WorkerID: "job", Question: "Choose scope"})
	f.loop.NudgeAfterWorkerJobTerminal(t.Context(), "session", "job", anchor.WorkerTaskFinished, "", "leg", env)
	pending, ok := f.loop.sessionPendingQueue("session").peek()
	if !ok || pending.completingJobID != "job" || pending.legID != "leg" || !pending.env.HasWorkerDecision() || pending.env.BatchSeq != 3 {
		t.Fatalf("queued wake lost its identity: %+v", pending)
	}
	f.drain(t, finish)
}

func TestWorkflowObservationDoesNotConsumeAnotherRun(t *testing.T) {
	f := newQueuedWaitFixture(t)
	observe := f.loop.ObservePrompt("session")
	pending := pendingLoopWake{wake: anchor.PhaseAdvanced, seq: f.loop.nudgeSeq.Add(1), runID: "run", revision: 5}
	observe(inject.CoordinatorTurnFrame{WorkflowRevision: 100, RunContext: api.CoordinatorRunContext{RunID: "another-run"}})
	if f.loop.wakeConsumed(t.Context(), "session", pending) {
		t.Fatal("a different workflow run consumed the wake")
	}
}

func TestWorkflowObservationRequiresTheEventRevisionEvenWhenQueuedBeforeCapture(t *testing.T) {
	f := newQueuedWaitFixture(t)
	pending := pendingLoopWake{wake: anchor.PhaseAdvanced, seq: f.loop.nudgeSeq.Add(1), runID: "run", revision: 5}
	f.loop.ObservePrompt("session")(inject.CoordinatorTurnFrame{WorkflowRevision: 4, RunContext: api.CoordinatorRunContext{RunID: "run"}})
	if f.loop.wakeConsumed(t.Context(), "session", pending) {
		t.Fatal("sequence order substituted for the required workflow revision")
	}
}

func TestObservedWakeStillDeliversUnconsumedGuidance(t *testing.T) {
	f := newQueuedWaitFixture(t)
	f.deps.HasQueuedKick = func(string, string) bool { return true }
	f.loop.SetDeps(f.deps)
	pending := pendingLoopWake{wake: anchor.PhaseAdvanced, inform: anchor.PhaseAdvanced, informHandled: true, seq: f.loop.nudgeSeq.Add(1), runID: "run", revision: 5}
	f.loop.ObservePrompt("session")(inject.CoordinatorTurnFrame{WorkflowRevision: 5, RunContext: api.CoordinatorRunContext{RunID: "run"}})
	if f.loop.wakeConsumed(t.Context(), "session", pending) {
		t.Fatal("queued guidance was discarded with observed board facts")
	}
}

func TestPendingQueueRetryKeepsItsPositionWithoutDuplicatingEvent(t *testing.T) {
	queue := &sessionNudgeQueue{}
	first := pendingLoopWake{wake: anchor.ProcessFinished, seq: 1, legID: "first"}
	second := pendingLoopWake{wake: anchor.ProcessFinished, seq: 2, legID: "second"}
	queue.push(first)
	queue.push(second)
	retry, _ := queue.pop()
	queue.push(retry)
	queue.push(retry)
	for _, want := range []string{"first", "second"} {
		got, ok := queue.pop()
		if !ok || got.legID != want {
			t.Fatalf("retried queue got %+v, want %s", got, want)
		}
	}
	if _, ok := queue.pop(); ok {
		t.Fatal("retry duplicated the event")
	}
}

func TestDeferredGuidanceIsQueuedBeforeObservationCanRetireItsWake(t *testing.T) {
	f := newQueuedWaitFixture(t)
	var informs atomic.Int32
	var queued atomic.Bool
	f.deps.QueueInform = func(context.Context, string, anchor.ID, anchor.Envelope) { informs.Add(1); queued.Store(true) }
	f.deps.HasQueuedKick = func(string, string) bool { return queued.Load() }
	f.deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		queued.Store(false)
		f.prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	f.loop.SetDeps(f.deps)
	f.loop.deferNudge(t.Context(), "session", pendingLoopWake{wake: anchor.PhaseAdvanced, inform: anchor.PhaseAdvanced})
	f.loop.ObservePrompt("session")(inject.CoordinatorTurnFrame{})
	f.loop.flushDeferredNudges(t.Context(), "session")
	f.loop.WaitForAsyncTurns(testutil.BoundedContext(t, 2*time.Second))
	if informs.Load() != 1 || f.prompts.Load() != 1 {
		t.Fatalf("guidance=%d prompts=%d", informs.Load(), f.prompts.Load())
	}
}

func TestQueueAdmissionPreventsPrematureUserTurnSettlement(t *testing.T) {
	f := newQueuedWaitFixture(t)
	// An entry has left the queue, but its admission decision is still in flight.
	f.loop.pendingDrain.Store("session", struct{}{})
	if !f.loop.HasPendingLoopWakes("session") {
		t.Fatal("admission was reported as quiescent")
	}
	release, claimed := f.loop.BeginUserTurnSettlement(t.Context(), "session")
	if claimed {
		release()
		t.Fatal("user turn settled while queue admission was in flight")
	}
	f.loop.pendingDrain.Delete("session")
	release, claimed = f.loop.BeginUserTurnSettlement(t.Context(), "session")
	if !claimed {
		t.Fatal("quiescent user turn could not settle")
	}
	release()
}

func TestMatchingWaitResultBypassesStaleBatchFilter(t *testing.T) {
	f := newQueuedWaitFixture(t)
	f.arm(t)
	f.deps.WorkflowSource = workflowFixturePorts(closedBatchLoopWF(3))
	f.deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return false }
	var resumes atomic.Int32
	f.deps.RunWaitResume = func(_ context.Context, _ string, delivery WaitDelivery) (*promptresult.Result, error) {
		if delivery.LeaseID != f.lease.ID || delivery.Condition.Kind != "process_done" {
			t.Errorf("unexpected wait result: %+v", delivery)
		}
		resumes.Add(1)
		return &promptresult.Result{}, delivery.Admitted()
	}
	f.loop.SetDeps(f.deps)
	f.loop.Nudge(t.Context(), "session", anchor.ProcessFinished, "", "target", anchor.Envelope{BatchSeq: 1, BatchSeqSet: true})
	f.loop.WaitForAsyncTurns(testutil.BoundedContext(t, 2*time.Second))
	if resumes.Load() != 1 || f.prompts.Load() != 0 || f.loop.IsSleeping("session") {
		t.Fatalf("stale-batch result: resumes=%d prompts=%d sleeping=%v", resumes.Load(), f.prompts.Load(), f.loop.IsSleeping("session"))
	}
}
