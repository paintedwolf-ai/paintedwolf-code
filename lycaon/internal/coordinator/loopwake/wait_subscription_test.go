package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/promptresult"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEnsureTimerBackstop(t *testing.T) {
	got := ensureTimerBackstop([]WaitTrigger{WaitTriggerProcessDone})
	if !hasWaitTriggerInSlice(got, WaitTriggerTimer) || !hasWaitTriggerInSlice(got, WaitTriggerProcessDone) {
		t.Fatalf("got %v want timer+process_done", got)
	}
	unchanged := ensureTimerBackstop([]WaitTrigger{WaitTriggerTimer, WaitTriggerScanDone})
	if len(unchanged) != 2 || unchanged[0] != WaitTriggerTimer {
		t.Fatalf("got %v want timer kept first", unchanged)
	}
	if ensureTimerBackstop(nil) != nil {
		t.Fatal("empty stays empty")
	}
}

func TestWaitSubscribesAllWorkersIdle(t *testing.T) {
	if waitSubscribesAllWorkersIdle([]WaitTrigger{WaitTriggerTimer}) {
		t.Fatal("timer-only must not subscribe all_workers_idle")
	}
	if !waitSubscribesAllWorkersIdle([]WaitTrigger{WaitTriggerTimer, WaitTriggerAllWorkersIdle}) {
		t.Fatal("expected all_workers_idle subscription")
	}
	if !waitSubscribesAllWorkersIdle(DefaultCoordinatorWaitTriggers(false)) {
		t.Fatal("default wait must subscribe all_workers_idle")
	}
}

func TestDefaultCoordinatorWaitTriggersGatesOverlayTrigger(t *testing.T) {
	clean := DefaultCoordinatorWaitTriggers(false)
	if hasWaitTriggerInSlice(clean, WaitTriggerOverlayPromote) {
		t.Fatalf("clean ledger must omit overlay trigger, got %v", clean)
	}
	pending := DefaultCoordinatorWaitTriggers(true)
	if !hasWaitTriggerInSlice(pending, WaitTriggerOverlayPromote) {
		t.Fatalf("pending overlays must include overlay trigger, got %v", pending)
	}
}

func TestResolveConditionsDefaultsToTimer(t *testing.T) {
	subscription, err := resolveConditions(nil)
	testutil.FailErr(t, "resolveConditions failed", err)
	if len(subscription.Conditions) != 0 || subscription.ExplicitConditions || len(subscription.Triggers) != 1 || subscription.Triggers[0] != WaitTriggerTimer {
		t.Fatalf("conditions=%v triggers=%v explicit=%v", subscription.Conditions, subscription.Triggers, subscription.ExplicitConditions)
	}
}

func hasWaitTriggerInSlice(triggers []WaitTrigger, want WaitTrigger) bool {
	for _, tr := range triggers {
		if tr == want {
			return true
		}
	}
	return false
}

func TestWaitEventMatchesNextWorkerDone(t *testing.T) {
	triggers := []WaitTrigger{WaitTriggerTimer, WaitTriggerNextWorkerDone}
	if !waitEventMatches(triggers, waitMatchInput{
		Wake:            anchor.WorkerTaskFinished,
		CompletingJobID: "job-1",
		CycleIdle:       false,
	}) {
		t.Fatal("expected next_worker_done match")
	}
	if waitEventMatches(triggers, waitMatchInput{
		Wake:      anchor.WorkerTaskFinished,
		CycleIdle: true,
	}) {
		t.Fatal("all_workers_idle not subscribed")
	}
}

func TestWaitEventMatchesAllWorkersIdleOnly(t *testing.T) {
	triggers := []WaitTrigger{WaitTriggerTimer, WaitTriggerAllWorkersIdle}
	if waitEventMatches(triggers, waitMatchInput{
		Wake:            anchor.WorkerTaskFinished,
		CompletingJobID: "job-1",
		CycleIdle:       false,
	}) {
		t.Fatal("per-job wake should not match all_workers_idle-only subscription")
	}
	if !waitEventMatches(triggers, waitMatchInput{
		Wake:      anchor.WorkerTaskFinished,
		CycleIdle: true,
	}) {
		t.Fatal("expected cycle idle match")
	}
}

func TestWaitEventMatchesNeedsDecisionBreaksAllWorkersIdlePark(t *testing.T) {
	// An explicit all_workers_idle condition parks without next_worker_done.
	// A decision request wakes the coordinator while the worker is held.
	triggers := []WaitTrigger{WaitTriggerTimer, WaitTriggerAllWorkersIdle}
	if !waitEventMatches(triggers, waitMatchInput{
		Wake:            anchor.WorkerTaskFinished,
		CompletingJobID: "job-decision",
		CycleIdle:       false,
		NeedsDecision:   true,
	}) {
		t.Fatal("needs_decision must match under all_workers_idle-only subscription")
	}
	if waitEventMatches(triggers, waitMatchInput{
		Wake:            anchor.WorkerTaskFinished,
		CompletingJobID: "job-complete",
		CycleIdle:       false,
		NeedsDecision:   false,
	}) {
		t.Fatal("ordinary complete must still defer under all_workers_idle-only")
	}
}

func TestLoopNeedsDecisionWakesThroughAllWorkersIdleWait(t *testing.T) {
	loop := NewLoopEngine()
	var prompts atomic.Int32
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle}, nil
	}
	deps.WorkflowSource = StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	}
	deps.WorkerCycleIdle = func(context.Context, *api.Session, string) (bool, error) {
		return false, nil // siblings still running
	}
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool {
		return false // mid-batch would otherwise skip_turn_rearm
	}
	loop.SetDeps(deps)
	loop.EnterSleep(context.Background(), "s1", time.Now().UTC().Add(30*time.Minute), "waiting for all workers", []WaitTrigger{WaitTriggerTimer, WaitTriggerAllWorkersIdle}, nil, SleepMoverHost)

	var env anchor.Envelope
	env.WithWorkerDecision(api.WorkerDecisionRequest{
		WorkerID:       "job-decision",
		ChildSessionID: "child-1",
		Question:       "Which scan path?",
		Options:        []string{"retry scan_list", "manual audit"},
		BlockerClass:   api.WorkerBlockerDecision,
	})
	loop.NudgeAfterWorkerJobTerminal(
		context.Background(),
		"s1",
		"job-decision",
		anchor.WorkerTaskFinished,
		anchor.WorkerTaskFinished,
		"",
		env,
	)
	testutil.WaitFor(t, 2*time.Second, func() bool { return prompts.Load() >= 1 })
	if loop.IsSleeping("s1") {
		t.Fatal("needs_decision must break sleep")
	}
}

func TestLoopSubscriptionFiltersPerJobWake(t *testing.T) {
	loop := NewLoopEngine()
	var prompts int
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle}, nil
	}
	deps.WorkerCycleIdle = func(context.Context, *api.Session, string) (bool, error) {
		return false, nil
	}
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts++
		return &promptresult.Result{}, nil
	}
	loop.SetDeps(deps)
	loop.EnterSleep(context.Background(), "s1", time.Now().UTC().Add(30*time.Minute), "batch scouts", []WaitTrigger{
		WaitTriggerTimer,
		WaitTriggerAllWorkersIdle,
	}, nil, SleepMoverHost)
	loop.NudgeAfterWorkerJobTerminal(context.Background(), "s1", "job-1", anchor.WorkerTaskFinished, anchor.WorkerTaskFinished, "", anchor.Envelope{})
	if prompts != 0 {
		t.Fatalf("prompts=%d want 0 filtered wake", prompts)
	}
	if !loop.IsSleeping("s1") {
		t.Fatal("sleep should stay armed when per-job wake not subscribed")
	}
}
