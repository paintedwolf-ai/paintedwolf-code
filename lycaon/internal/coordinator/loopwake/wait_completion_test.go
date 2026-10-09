package loopwake

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"sync/atomic"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func completionWaitArgs() map[string]any {
	return map[string]any{"until_complete": true, "conditions": []any{
		map[string]any{"kind": "process_done", "handles": []any{"command-1"}},
	}}
}

func completionWaitFixture(t *testing.T) (*LoopEngine, *awaitstore.Store, *tools.DefaultRegistry) {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "s1", testdbseed.DefaultProjectID)
	store := &awaitstore.Store{DB: database}
	loop := NewLoopEngine()
	t.Cleanup(func() { loop.ForgetSession(context.Background(), "s1") })
	deps := processCycleLoopDeps(true)
	deps.ProcessState = func(sessionID, handle string) (bool, bool) {
		return sessionID == "s1" && handle == "command-1", true
	}
	loop.SetDeps(deps)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register completion wait", RegisterWaitTool(reg, loop, WaitToolDeps{Store: store, RuntimeContext: t.Context()}))
	return loop, store, reg
}

func TestCompletionWaitRejectsUnownedOrUnboundedConditions(t *testing.T) {
	for _, name := range []string{"missing", "anonymous", "foreign", "mixed", "boolean"} {
		t.Run(name, func(t *testing.T) {
			loop, _, reg := completionWaitFixture(t)
			args := completionWaitArgs()
			switch name {
			case "missing":
				delete(args, "conditions")
			case "anonymous":
				args["conditions"] = []any{map[string]any{"kind": "process_done"}}
			case "foreign":
				args["conditions"] = []any{map[string]any{"kind": "process_done", "handles": []any{"foreign"}}}
			case "mixed":
				args["conditions"] = append(args["conditions"].([]any), map[string]any{"kind": "scan_done"})
			case "boolean":
				args["until_complete"] = "true"
			}
			_, err := reg.Run(t.Context(), "wait", args, tools.ToolContext{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID})
			if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
				t.Fatalf("invalid completion wait = %v", err)
			}
			if loop.IsSleeping("s1") {
				t.Fatal("rejected wait armed sleep")
			}
		})
	}
}

func TestCompletionWaitAllowsBoundedTimeout(t *testing.T) {
	loop, store, reg := completionWaitFixture(t)
	tctx := tools.ToolContext{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID}
	args := completionWaitArgs()
	args["timeout_ms"] = 15000
	out, err := reg.Run(t.Context(), "wait", args, tctx)
	testutil.FailErr(t, "park bounded completion wait", err)
	var result WaitToolResult
	testutil.FailErr(t, "decode bounded completion wait", json.Unmarshal([]byte(out), &result))
	if !result.UntilComplete || result.WakeAt == "" || result.TimeoutMS != 15000 {
		t.Fatalf("bounded completion wait invalid result: %+v", result)
	}
	state := loop.sleepState("s1")
	state.mu.Lock()
	hasTimer := state.timer != nil
	until := state.until
	untilComplete := state.untilComplete
	state.mu.Unlock()
	if !hasTimer || until.IsZero() || !untilComplete {
		t.Fatalf("bounded completion wait state invalid: hasTimer=%v until=%v untilComplete=%v", hasTimer, until, untilComplete)
	}
	lease, found, err := store.ForSession(t.Context(), "s1")
	testutil.FailErr(t, "read bounded completion wait lease", err)
	if !found || lease.Deadline.IsZero() {
		t.Fatalf("bounded completion wait deadline not persisted: %+v", lease)
	}
}

func completionSleepState(loop *LoopEngine, sessionID string) (untilComplete, hasTimer bool, until time.Time) {
	state := loop.sleepState(sessionID)
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.untilComplete, state.timer != nil, state.until
}

func TestBoundedCompletionWaitResumesFromDurableLease(t *testing.T) {
	loop, store, reg := completionWaitFixture(t)
	tctx := tools.ToolContext{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID}
	args := completionWaitArgs()
	args["timeout_ms"] = 600000
	_, err := reg.Run(t.Context(), "wait", args, tctx)
	testutil.FailErr(t, "park bounded completion wait", err)
	loop.InterruptSleep(t.Context(), "s1")

	// A fresh engine has no runtime subscription, so resume reads the durable lease.
	restarted := NewLoopEngine()
	t.Cleanup(func() { restarted.ForgetSession(context.Background(), "s1") })
	restarted.SetDeps(loop.loopDeps())
	restartedReg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register restarted wait", RegisterWaitTool(restartedReg, restarted, WaitToolDeps{Store: store, RuntimeContext: t.Context()}))
	out, err := restartedReg.Run(t.Context(), "wait", map[string]any{"resume": true}, tctx)
	testutil.FailErr(t, "resume bounded completion wait", err)
	var result WaitToolResult
	testutil.FailErr(t, "decode resumed bounded completion", json.Unmarshal([]byte(out), &result))
	if !result.UntilComplete || !result.Resumed || result.WakeAt == "" {
		t.Fatalf("resume lost bounded completion mode: %+v", result)
	}
	untilComplete, hasTimer, until := completionSleepState(restarted, "s1")
	if !untilComplete || !hasTimer || until.IsZero() {
		t.Fatalf("resumed state: untilComplete=%v timer=%v until=%v", untilComplete, hasTimer, until)
	}
	lease, found, err := store.ForSession(t.Context(), "s1")
	testutil.FailErr(t, "read resumed lease", err)
	if !found || !lease.UntilComplete || lease.Deadline.IsZero() {
		t.Fatalf("resumed lease lost completion mode: %+v", lease)
	}
}

func TestBoundedCompletionWaitResumesFromRuntimeSubscription(t *testing.T) {
	loop, _, reg := completionWaitFixture(t)
	tctx := tools.ToolContext{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID}
	args := completionWaitArgs()
	args["timeout_ms"] = 600000
	_, err := reg.Run(t.Context(), "wait", args, tctx)
	testutil.FailErr(t, "park bounded completion wait", err)
	loop.breakSleep(t.Context(), "s1", "nudge", true)
	out, err := reg.Run(t.Context(), "wait", map[string]any{"resume": true}, tctx)
	testutil.FailErr(t, "resume bounded completion wait", err)
	var result WaitToolResult
	testutil.FailErr(t, "decode resumed bounded completion", json.Unmarshal([]byte(out), &result))
	if !result.UntilComplete || result.WakeAt == "" {
		t.Fatalf("runtime resume dropped the deadline: %+v", result)
	}
	if untilComplete, hasTimer, _ := completionSleepState(loop, "s1"); !untilComplete || !hasTimer {
		t.Fatalf("runtime resume state: untilComplete=%v timer=%v", untilComplete, hasTimer)
	}
}

func TestRecoveredBoundedCompletionWaitKeepsMode(t *testing.T) {
	loop, store, _ := completionWaitFixture(t)
	_, err := store.Arm(t.Context(), awaitstore.Lease{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID,
		UntilComplete: true, Deadline: time.Now().UTC().Add(10 * time.Minute),
		Conditions: []awaitstore.Condition{{Kind: "process_done", Handles: []string{"command-1"}}}})
	testutil.FailErr(t, "persist bounded completion wait", err)
	testutil.FailErr(t, "recover bounded completion wait", RecoverWaitLeases(t.Context(), loop, store))
	if untilComplete, hasTimer, until := completionSleepState(loop, "s1"); !untilComplete || !hasTimer || until.IsZero() {
		t.Fatalf("recovered state: untilComplete=%v timer=%v until=%v", untilComplete, hasTimer, until)
	}
}

func TestSkipRearmKeepsCompletionMode(t *testing.T) {
	loop, _, reg := completionWaitFixture(t)
	_, err := reg.Run(t.Context(), "wait", completionWaitArgs(), tools.ToolContext{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID})
	testutil.FailErr(t, "park completion wait", err)
	loop.rearmSleepAfterSkip(t.Context(), "s1")
	if untilComplete, hasTimer, _ := completionSleepState(loop, "s1"); !untilComplete || hasTimer {
		t.Fatalf("skip re-arm changed completion mode: untilComplete=%v timer=%v", untilComplete, hasTimer)
	}
}

func TestCompletionWaitHasNoTimerAndResumesMode(t *testing.T) {
	loop, store, reg := completionWaitFixture(t)
	tctx := tools.ToolContext{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID}
	out, err := reg.Run(t.Context(), "wait", completionWaitArgs(), tctx)
	testutil.FailErr(t, "park completion wait", err)
	var result WaitToolResult
	testutil.FailErr(t, "decode completion wait", json.Unmarshal([]byte(out), &result))
	if !result.UntilComplete || result.WakeAt != "" || result.TimeoutMS != 0 {
		t.Fatalf("completion wait has deadline: %+v", result)
	}
	state := loop.sleepState("s1")
	state.mu.Lock()
	armedTomorrow := sleepArmedLocked(state, time.Now().Add(24*time.Hour))
	hasTimer := state.timer != nil
	state.mu.Unlock()
	if !armedTomorrow || hasTimer {
		t.Fatalf("completion wait expires: armedTomorrow=%v timer=%v", armedTomorrow, hasTimer)
	}
	lease, found, err := store.ForSession(t.Context(), "s1")
	testutil.FailErr(t, "read completion wait", err)
	if !found || !lease.Deadline.IsZero() {
		t.Fatalf("completion wait deadline persisted: %+v", lease)
	}
	loop.InterruptSleep(t.Context(), "s1")
	if loop.IsSleeping("s1") {
		t.Fatal("user interruption left completion wait armed")
	}
	out, err = reg.Run(t.Context(), "wait", map[string]any{"resume": true}, tctx)
	testutil.FailErr(t, "resume completion wait", err)
	result = WaitToolResult{}
	testutil.FailErr(t, "decode resumed wait", json.Unmarshal([]byte(out), &result))
	if !result.UntilComplete || !result.Resumed || len(result.Conditions) != 1 || result.Conditions[0].Handles[0] != "command-1" {
		t.Fatalf("resume lost completion mode: %+v", result)
	}
	loop.InterruptSleep(t.Context(), "s1")
	out, err = reg.Run(t.Context(), "wait", map[string]any{"resume": true, "timeout_ms": 60000}, tctx)
	testutil.FailErr(t, "replace completion mode", err)
	result = WaitToolResult{}
	testutil.FailErr(t, "decode bounded replacement", json.Unmarshal([]byte(out), &result))
	if result.UntilComplete || result.WakeAt == "" || !hasWaitTrigger(loop.WaitSubscriptionForTest("s1"), WaitTriggerTimer) {
		t.Fatalf("explicit timeout did not replace completion mode: %+v", result)
	}
}

func TestCompletionWaitDeliversTerminalOrLostProcessOnce(t *testing.T) {
	for _, event := range []string{"exit", "during-arm", "recovery"} {
		t.Run(event, func(t *testing.T) {
			loop, store, reg := completionWaitFixture(t)
			deps := loop.loopDeps()
			var deliveries atomic.Int32
			deps.RunWaitResume = func(_ context.Context, _ string, delivery WaitDelivery) (*promptresult.Result, error) {
				if event == "recovery" && delivery.Condition.Outcome != "unavailable" {
					t.Errorf("lost process winner = %+v", delivery.Condition)
				}
				if err := delivery.Admitted(); err != nil {
					return nil, err
				}
				deliveries.Add(1)
				return &promptresult.Result{}, nil
			}
			if event == "during-arm" {
				var exited atomic.Bool
				deps.ProcessState = func(string, string) (bool, bool) { return true, !exited.Load() }
				deps.PublishWaitLease = func(_ context.Context, _ string, lease WaitLease) {
					if lease.Active {
						exited.Store(true)
					}
				}
			}
			loop.SetDeps(deps)
			if event == "recovery" {
				_, err := store.Arm(t.Context(), awaitstore.Lease{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID, UntilComplete: true,
					Conditions: []awaitstore.Condition{{Kind: "process_done", Handles: []string{"command-1"}}}})
				testutil.FailErr(t, "persist wait before restart", err)
			} else {
				_, err := reg.Run(t.Context(), "wait", completionWaitArgs(), tools.ToolContext{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID})
				testutil.FailErr(t, "park terminal process wait", err)
			}
			switch event {
			case "exit":
				loop.NudgeProcessFinished(t.Context(), "s1", "unrelated", anchor.Envelope{})
				if !loop.IsSleeping("s1") {
					t.Fatal("unrelated process ended wait")
				}
				loop.NudgeProcessFinished(t.Context(), "s1", "command-1", anchor.Envelope{})
			case "recovery":
				deps.ProcessState = func(string, string) (bool, bool) { return false, false }
				loop.SetDeps(deps)
				testutil.FailErr(t, "recover lost process wait", RecoverWaitLeases(t.Context(), loop, store))
			}
			testutil.WaitFor(t, time.Second, func() bool { return deliveries.Load() == 1 })
			loop.WaitForAsyncTurns(testutil.BoundedContext(t, time.Second))
			if loop.IsSleeping("s1") {
				t.Fatal("terminal process left agent sleeping")
			}
			testutil.FailErr(t, "recover delivered completion", RecoverWaitLeases(t.Context(), loop, store))
			loop.WaitForAsyncTurns(testutil.BoundedContext(t, time.Second))
			if deliveries.Load() != 1 {
				t.Fatalf("completion delivered %d times", deliveries.Load())
			}
		})
	}
}

func TestCompletionWaitReconciliationKeepsLiveProcessParked(t *testing.T) {
	loop, store, reg := completionWaitFixture(t)
	_, err := reg.Run(t.Context(), "wait", completionWaitArgs(), tools.ToolContext{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID})
	testutil.FailErr(t, "park live process wait", err)
	lease, found, err := store.ForSession(t.Context(), "s1")
	testutil.FailErr(t, "read live process wait", err)
	if !found {
		t.Fatal("live process wait missing")
	}
	if done := reconcileWaitConditions(t.Context(), loop, store, lease); done || !loop.IsSleeping("s1") {
		t.Fatal("host liveness check ended a live process wait")
	}
	loop.InterruptSleep(t.Context(), "s1")
	if done := reconcileWaitConditions(t.Context(), loop, store, lease); !done {
		t.Fatal("interrupted wait left its host monitor active")
	}
	if loop.HasPendingLoopWakes("s1") {
		t.Fatal("liveness check or interruption scheduled a model wake")
	}
}

func TestCompletionWaitReturnsAlreadyFinishedWithoutParking(t *testing.T) {
	loop, store, reg := completionWaitFixture(t)
	deps := loop.loopDeps()
	deps.ProcessState = func(string, string) (bool, bool) { return true, false }
	loop.SetDeps(deps)
	out, err := reg.Run(t.Context(), "wait", completionWaitArgs(), tools.ToolContext{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID})
	testutil.FailErr(t, "wait on finished process", err)
	var result WaitToolResult
	testutil.FailErr(t, "decode finished process", json.Unmarshal([]byte(out), &result))
	if result.Status != "process_done" || len(result.Conditions) != 1 || result.Conditions[0].Outcome != "satisfied" || result.Conditions[0].Handles[0] != "command-1" {
		t.Fatalf("finished process result = %+v", result)
	}
	active, err := store.Active(t.Context())
	testutil.FailErr(t, "read finished process leases", err)
	if len(active) != 0 || loop.IsSleeping("s1") || loop.HasPendingLoopWakes("s1") {
		t.Fatal("finished process created a parked wait or model wake")
	}
}

// A next_worker_done wait that names task ids ignores other workers finishing.
func TestNextWorkerDoneWaitSelectsNamedTasks(t *testing.T) {
	subscription, err := resolveConditions([]any{map[string]any{"kind": "next_worker_done", "handles": []any{"job-2"}}})
	testutil.FailErr(t, "resolve worker-selective wait", err)
	if len(subscription.WorkerHandles) != 1 || len(subscription.ProcessHandles) != 0 {
		t.Fatalf("subscription = %+v", subscription)
	}
	triggers, _ := triggersFromConditions(subscription.Conditions)
	in := waitMatchInput{Wake: anchor.WorkerTaskFinished, CompletingJobID: "job-1", WorkerHandles: subscription.WorkerHandles}
	if waitEventMatches(triggers, in) {
		t.Fatal("an unnamed worker woke the wait")
	}
	in.CompletingJobID = "job-2"
	condition, matched := waitConditionForWake(triggers, in)
	if !matched || condition.Kind != "next_worker_done" || len(condition.Handles) != 1 || condition.Handles[0] != "job-2" {
		t.Fatalf("named worker did not settle the wait: matched=%v condition=%+v", matched, condition)
	}
	restored := conditionsFromTriggers(triggers, nil, subscription.WorkerHandles)
	if got := workerHandlesFromConditions(restored); len(got) != 1 || got[0] != "job-2" {
		t.Fatalf("worker selection lost across the durable lease: %v", got)
	}
}
