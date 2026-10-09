package loopwake

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCoordinatorWaitDeliverySurvivesOptionalWakeGates(t *testing.T) {
	for _, event := range []string{"deadline", "process", "recovery"} {
		t.Run(event, func(t *testing.T) {
			database := testdbfixture.Open(t, "store.db")
			const id = "waiting-coordinator"
			testdbseed.InsertSession(t, database, id, testdbseed.DefaultProjectID)
			store := &awaitstore.Store{DB: database}
			deadline := time.Now().UTC().Add(time.Minute)
			if event == "deadline" {
				deadline = time.Now().UTC().Add(25 * time.Millisecond)
			}
			lease, err := store.Arm(t.Context(), awaitstore.Lease{SessionID: id, ProjectID: testdbseed.DefaultProjectID,
				ToolCallID: "wait-call", ProfileID: "implement", Deadline: deadline,
				Conditions: []awaitstore.Condition{{Kind: "process_done", Handles: []string{"server"}}}})
			testutil.FailErr(t, "arm coordinator wait", err)
			loop := NewLoopEngine()
			loop.SetWaitStore(store)
			t.Cleanup(func() { loop.ForgetSession(context.Background(), id) })
			deps := loopDepsForTest()
			deps.GetSession = func(context.Context, string) (*api.Session, error) {
				return &api.Session{ID: id, Status: api.SessionStatusBusy}, nil
			}
			deps.WorkflowSource = workflowFixturePorts(closedBatchLoopWF(3))
			deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return false }
			deps.WorkerCycleIdle = func(context.Context, *api.Session, string) (bool, error) { return false, nil }
			var deliveries atomic.Int32
			deps.RunWaitResume = func(_ context.Context, sessionID string, delivery WaitDelivery) (*promptresult.Result, error) {
				leaseID, winner, admitted := delivery.LeaseID, delivery.Condition, delivery.Admitted
				if sessionID != id || leaseID != lease.ID {
					t.Errorf("delivery target = %s/%s", sessionID, leaseID)
				}
				if event == "process" && winner.Kind != "process_done" {
					t.Errorf("process winner = %+v", winner)
				}
				if event != "process" && winner.Outcome != "timed_out" {
					t.Errorf("deadline winner = %+v", winner)
				}
				if err := admitted(); err != nil {
					return nil, err
				}
				deliveries.Add(1)
				return &promptresult.Result{}, nil
			}
			loop.SetDeps(deps)
			finish := loop.BeginPromptExecution(t.Context(), id)
			defer finish()
			if event == "recovery" {
				_, err = store.SettleLease(t.Context(), lease.ID, "timed_out", awaitstore.Condition{Kind: "timer", Outcome: "timed_out"})
				testutil.FailErr(t, "settle before recovery", err)
				testutil.FailErr(t, "recover pending wait", RecoverWaitLeases(t.Context(), loop, store))
			} else {
				loop.EnterSleep(t.Context(), id, deadline, "server startup", []WaitTrigger{WaitTriggerTimer, WaitTriggerProcessDone}, []string{"server"}, SleepMoverHost)
				if event == "process" {
					loop.NudgeProcessFinished(t.Context(), id, "server", anchor.Envelope{})
				}
			}
			testutil.WaitFor(t, time.Second, func() bool { _, ready := loop.waitWinner(id); return ready })
			if deliveries.Load() != 0 {
				t.Fatal("wait delivery overlapped active execution")
			}
			finish()
			testutil.WaitFor(t, time.Second, func() bool { return deliveries.Load() == 1 })
			loop.WaitForAsyncTurns(testutil.BoundedContext(t, time.Second))
			pending, err := store.PendingAgentResumes(t.Context())
			testutil.FailErr(t, "read pending waits", err)
			if len(pending) != 0 {
				t.Fatalf("delivered wait remains pending: %+v", pending)
			}
			testutil.FailErr(t, "recover acknowledged wait", RecoverWaitLeases(t.Context(), loop, store))
			loop.WaitForAsyncTurns(testutil.BoundedContext(t, time.Second))
			if deliveries.Load() != 1 {
				t.Fatalf("deliveries = %d", deliveries.Load())
			}
		})
	}
}

func TestWaitDeliveryRetriesMissingAcknowledgement(t *testing.T) {
	loop := NewLoopEngine()
	const id = "unacknowledged"
	t.Cleanup(func() { loop.ForgetSession(context.Background(), id) })
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) { return &api.Session{ID: id}, nil }
	var attempts atomic.Int32
	deps.RunWaitResume = func(_ context.Context, _ string, delivery WaitDelivery) (*promptresult.Result, error) {
		admitted := delivery.Admitted
		if attempts.Add(1) == 1 {
			return &promptresult.Result{}, nil
		}
		return &promptresult.Result{}, admitted()
	}
	loop.SetDeps(deps)
	loop.rememberWaitWinner(id, "lease", awaitstore.Condition{Kind: "timer", Outcome: "timed_out"})
	if !loop.HasPendingLoopWakes(id) {
		t.Fatal("undelivered result did not keep the turn open")
	}
	loop.Nudge(t.Context(), id, anchor.WaitTimerFired, anchor.WaitTimerFired, "", anchor.Envelope{})
	testutil.WaitFor(t, 3*time.Second, func() bool { return attempts.Load() == 2 && !loop.HasPendingLoopWakes(id) })
	loop.WaitForAsyncTurns(testutil.BoundedContext(t, time.Second))
	if attempts.Load() != 2 {
		t.Fatalf("admission attempts = %d", attempts.Load())
	}
}

func TestUserInputRetiresPendingWaitDelivery(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	const id = "interrupted-wait"
	testdbseed.InsertSession(t, database, id, testdbseed.DefaultProjectID)
	store := &awaitstore.Store{DB: database}
	lease, err := store.Arm(t.Context(), awaitstore.Lease{SessionID: id, ProjectID: testdbseed.DefaultProjectID, Deadline: time.Now().UTC()})
	testutil.FailErr(t, "arm wait", err)
	winner := awaitstore.Condition{Kind: "timer", Outcome: "timed_out"}
	_, err = store.SettleLease(t.Context(), lease.ID, "timed_out", winner)
	testutil.FailErr(t, "settle wait", err)
	loop := NewLoopEngine()
	loop.SetWaitStore(store)
	loop.rememberWaitWinner(id, lease.ID, winner)
	loop.InterruptSleep(t.Context(), id)
	if loop.HasPendingLoopWakes(id) {
		t.Fatal("interrupted wait kept the turn open")
	}
	pending, err := store.PendingAgentResumes(t.Context())
	testutil.FailErr(t, "read pending waits", err)
	if len(pending) != 0 {
		t.Fatalf("interrupted result would resume after restart: %+v", pending)
	}
}

func TestWaitDeliveryRetiresOnlyObservedWakeFacts(t *testing.T) {
	for _, observed := range []bool{false, true} {
		t.Run(map[bool]string{false: "admission only", true: "successful model response"}[observed], func(t *testing.T) {
			const id = "wait-wake-order"
			loop := NewLoopEngine()
			t.Cleanup(func() { loop.ForgetSession(context.Background(), id) })
			deps := loopDepsForTest()
			deps.GetSession = func(context.Context, string) (*api.Session, error) {
				return &api.Session{ID: id, Status: api.SessionStatusIdle}, nil
			}
			deps.WorkflowSource = workflowFixturePorts(StubLoopWF{run: &api.WorkflowRun{ID: "run", Status: api.WorkflowRunStatusRunning}})
			var prompts atomic.Int32
			deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
				prompts.Add(1)
				return &promptresult.Result{}, nil
			}
			deps.RunWaitResume = func(_ context.Context, _ string, delivery WaitDelivery) (*promptresult.Result, error) {
				if err := delivery.Admitted(); err != nil {
					return nil, err
				}
				observe := loop.ObservePrompt(id)
				if observed {
					observe(inject.CoordinatorTurnFrame{})
				}
				return &promptresult.Result{}, nil
			}
			loop.SetDeps(deps)
			queue := loop.sessionPendingQueue(id)
			queue.push(pendingLoopWake{wake: anchor.PhaseAdvanced, seq: loop.nudgeSeq.Add(1)})
			loop.rememberWaitWinner(id, "lease", awaitstore.Condition{Kind: "timer", Outcome: "timed_out"})
			loop.Nudge(t.Context(), id, anchor.WaitTimerFired, anchor.WaitTimerFired, "", anchor.Envelope{})
			loop.WaitForAsyncTurns(testutil.BoundedContext(t, time.Second))
			loop.DrainPending(t.Context(), id)
			wantPrompts := int32(1)
			if observed {
				wantPrompts = 0
			}
			if prompts.Load() != wantPrompts {
				t.Fatalf("earlier wake prompts=%d, want %d", prompts.Load(), wantPrompts)
			}
			queue.push(pendingLoopWake{wake: anchor.PhaseAdvanced, seq: loop.nudgeSeq.Add(1)})
			loop.DrainPending(t.Context(), id)
			if prompts.Load() != wantPrompts+1 {
				t.Fatal("wait resume suppressed a later wake")
			}
		})
	}
}
