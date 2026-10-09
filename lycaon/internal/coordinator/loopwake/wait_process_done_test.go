package loopwake

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

func TestResolveConditionsAcceptsProcessDone(t *testing.T) {
	subscription, err := resolveConditions([]any{map[string]any{"kind": "process_done"}})
	if err != nil {
		t.Fatalf("resolveConditions: %v", err)
	}
	if !hasWaitTriggerInSlice(subscription.Triggers, WaitTriggerProcessDone) {
		t.Fatalf("triggers = %v want process_done", subscription.Triggers)
	}
}

func TestDefaultSubscriptionsExcludeProcessDone(t *testing.T) {
	if hasWaitTriggerInSlice(DefaultCoordinatorWaitTriggers(true), WaitTriggerProcessDone) {
		t.Fatal("default wait set must not include process_done")
	}
}

func TestWaitEventMatchesProcessDoneRequiresSubscription(t *testing.T) {
	subscribed := []WaitTrigger{WaitTriggerTimer, WaitTriggerProcessDone}
	if !waitEventMatches(subscribed, waitMatchInput{Wake: anchor.ProcessFinished}) {
		t.Fatal("process_done nudge must wake a process_done subscription")
	}
	if waitEventMatches([]WaitTrigger{WaitTriggerTimer}, waitMatchInput{Wake: anchor.ProcessFinished}) {
		t.Fatal("process_done nudge must not wake without subscription")
	}
	if waitEventMatches(nil, waitMatchInput{Wake: anchor.ProcessFinished}) {
		t.Fatal("process_done nudge must not wake the default wait set")
	}
}

func TestNudgeProcessFinishedBreaksSubscribedSleepOnly(t *testing.T) {
	loop := NewLoopEngine()
	deps := loopDepsForTest()
	deps.GetSession = func(_ context.Context, sessionID string) (*api.Session, error) {
		return &api.Session{ID: sessionID, Status: api.SessionStatusIdle}, nil
	}
	loop.SetDeps(deps)
	deadline := time.Now().UTC().Add(10 * time.Minute)
	loop.Waits.EnterSleep(context.Background(), "proc-waiter", deadline, "waiting for command", []WaitTrigger{WaitTriggerTimer, WaitTriggerProcessDone}, nil, SleepMoverHost)
	loop.Waits.EnterSleep(context.Background(), "worker-waiter", deadline, "waiting for workers", DefaultCoordinatorWaitTriggers(false), nil, SleepMoverHost)

	loop.Nudges.NudgeProcessFinished(context.Background(), "proc-waiter", "handle-1", anchor.Envelope{})
	loop.Nudges.NudgeProcessFinished(context.Background(), "worker-waiter", "handle-1", anchor.Envelope{})

	if loop.Waits.IsSleeping("proc-waiter") {
		t.Fatal("process_done nudge must break a subscribed sleep")
	}
	if !loop.Waits.IsSleeping("worker-waiter") {
		t.Fatal("process_done nudge must not break an unsubscribed sleep")
	}
}

// Internal event-only parks do not arm a timer; the event owns their lifetime.
func TestProcessDoneOnlySleepSurvivesPastAdvisoryDeadline(t *testing.T) {
	loop := NewLoopEngine()
	deps := loopDepsForTest()
	deps.GetSession = func(_ context.Context, sessionID string) (*api.Session, error) {
		return &api.Session{ID: sessionID, Status: api.SessionStatusIdle}, nil
	}
	loop.SetDeps(deps)
	// The command remains live after its advisory deadline.
	past := time.Now().UTC().Add(-2 * time.Minute)
	loop.Waits.EnterSleep(
		context.Background(), "proc-waiter", past, "Wait for the test command to finish.",
		[]WaitTrigger{WaitTriggerProcessDone}, []string{"handle-1"}, SleepMoverHost,
	)
	if !loop.Waits.IsSleeping("proc-waiter") {
		t.Fatal("process_done-only sleep must stay armed after the advisory wake_at")
	}
	if !loop.Waits.SessionSleepingOnProcess("proc-waiter", "handle-1") {
		t.Fatal("exact handle must still match after the advisory wake_at")
	}
	loop.Nudges.NudgeProcessFinished(context.Background(), "proc-waiter", "handle-1", anchor.Envelope{})
	if loop.Waits.IsSleeping("proc-waiter") {
		t.Fatal("process exit must break the event-only sleep")
	}
}

func TestNudgeProcessFinishedMatchesExactHandleSelection(t *testing.T) {
	loop := NewLoopEngine()
	deps := loopDepsForTest()
	deps.GetSession = func(_ context.Context, sessionID string) (*api.Session, error) {
		return &api.Session{ID: sessionID, Status: api.SessionStatusIdle}, nil
	}
	loop.SetDeps(deps)
	deadline := time.Now().UTC().Add(10 * time.Minute)
	loop.Waits.EnterSleep(
		context.Background(), "proc-waiter", deadline, "waiting for command",
		[]WaitTrigger{WaitTriggerTimer, WaitTriggerProcessDone}, []string{"wanted-handle"}, SleepMoverHost,
	)

	loop.Nudges.NudgeProcessFinished(context.Background(), "proc-waiter", "other-handle", anchor.Envelope{})
	if !loop.Waits.IsSleeping("proc-waiter") {
		t.Fatal("a different command must not break an exact-handle wait")
	}
	loop.Nudges.NudgeProcessFinished(context.Background(), "proc-waiter", "wanted-handle", anchor.Envelope{})
	if loop.Waits.IsSleeping("proc-waiter") {
		t.Fatal("the selected command must break the exact-handle wait")
	}
}

func processCycleLoopDeps(running bool) LoopDeps {
	deps := idleWaitLoopDeps()
	deps.ProcessRunning = func(string, []string) bool { return running }
	return deps
}

func TestWaitProcessDoneAlreadySatisfiedWhenNothingRunning(t *testing.T) {
	loop := NewLoopEngine()
	loop.SetDeps(processCycleLoopDeps(false))
	reg := tools.NewDefaultRegistry()
	if err := RegisterWaitTool(reg, loop.Subscriptions, WaitToolDeps{}); err != nil {
		t.Fatalf("RegisterWaitTool: %v", err)
	}
	out, err := reg.Run(context.Background(), "wait", map[string]any{
		"conditions": []any{map[string]any{"kind": "process_done"}},
	}, tools.ToolContext{
		SessionID: "s1",
		Agent:     orchestration.ProfileCoordinator,
	})
	if err != nil {
		t.Fatalf("wait(process_done) with nothing running: %v", err)
	}
	var result WaitToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Status != "process_done" {
		t.Fatalf("status = %q want process_done", result.Status)
	}
	if loop.Waits.IsSleeping("s1") {
		t.Fatal("must not arm sleep when nothing is running")
	}
}

func TestWaitProcessDoneArmsSleepWhileRunning(t *testing.T) {
	loop := NewLoopEngine()
	loop.SetDeps(processCycleLoopDeps(true))
	reg := tools.NewDefaultRegistry()
	if err := RegisterWaitTool(reg, loop.Subscriptions, WaitToolDeps{}); err != nil {
		t.Fatalf("RegisterWaitTool: %v", err)
	}
	invocationOut := &tools.ToolInvocationOut{}
	out, err := reg.Run(context.Background(), "wait", map[string]any{
		"timeout_ms": 180000,
		"conditions": []any{map[string]any{"kind": "process_done"}},
		"reason":     "waiting for the build to finish",
	}, tools.ToolContext{
		SessionID: "s1",
		Agent:     orchestration.ProfileCoordinator,
		Out:       invocationOut,
	})
	if err != nil {
		t.Fatalf("wait(process_done) with a running command: %v", err)
	}
	if !WaitCompletionEndsCycle("wait", invocationOut.Completion) {
		t.Fatalf("output = %q", out)
	}
	if !loop.Waits.IsSleeping("s1") {
		t.Fatal("expected sleeping while a command is running")
	}
	if !hasWaitTrigger(waitSubscriptionForTest(loop.Subscriptions, "s1"), WaitTriggerProcessDone) {
		t.Fatalf("armed triggers = %v want process_done", waitSubscriptionForTest(loop.Subscriptions, "s1"))
	}
}

func TestWaitProcessDoneOnlyAutoAddsTimerBackstop(t *testing.T) {
	loop := NewLoopEngine()
	loop.SetDeps(processCycleLoopDeps(true))
	reg := tools.NewDefaultRegistry()
	if err := RegisterWaitTool(reg, loop.Subscriptions, WaitToolDeps{}); err != nil {
		t.Fatalf("RegisterWaitTool: %v", err)
	}
	out, err := reg.Run(context.Background(), "wait", map[string]any{
		"timeout_ms": 120000,
		"conditions": []any{map[string]any{"kind": "process_done", "handles": []any{"handle-1"}}},
		"reason":     "Wait for the repository's short test command to finish.",
	}, tools.ToolContext{
		SessionID: "s1",
		Agent:     orchestration.ProfileCoordinator,
	})
	if err != nil {
		t.Fatalf("wait(process_done) without timer: %v", err)
	}
	var result WaitToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Status != "parked" {
		t.Fatalf("status = %q want parked", result.Status)
	}
	if result.TimeoutMS != 120000 {
		t.Fatalf("timeout_ms = %d want 120000", result.TimeoutMS)
	}
	if len(result.Conditions) != 1 || result.Conditions[0].Kind != string(WaitTriggerProcessDone) {
		t.Fatalf("result conditions = %v want process_done", result.Conditions)
	}
	armed := waitSubscriptionForTest(loop.Subscriptions, "s1")
	if !hasWaitTrigger(armed, WaitTriggerTimer) || !hasWaitTrigger(armed, WaitTriggerProcessDone) {
		t.Fatalf("armed triggers = %v want timer+process_done", armed)
	}
	if !loop.Waits.IsSleeping("s1") {
		t.Fatal("expected sleeping with a real timer backstop")
	}
}

func TestWaitProcessDoneFastPathUsesExactHandles(t *testing.T) {
	loop := NewLoopEngine()
	var selected []string
	deps := idleWaitLoopDeps()
	deps.ProcessRunning = func(_ string, handles []string) bool {
		selected = append([]string(nil), handles...)
		return false
	}
	loop.SetDeps(deps)
	reg := tools.NewDefaultRegistry()
	if err := RegisterWaitTool(reg, loop.Subscriptions, WaitToolDeps{}); err != nil {
		t.Fatalf("RegisterWaitTool: %v", err)
	}
	out, err := reg.Run(context.Background(), "wait", map[string]any{
		"conditions": []any{map[string]any{"kind": "process_done", "handles": []any{"command-2"}}},
	}, tools.ToolContext{SessionID: "s1", Agent: orchestration.ProfileCoordinator})
	if err != nil {
		t.Fatalf("wait exact process handle: %v", err)
	}
	if len(selected) != 1 || selected[0] != "command-2" {
		t.Fatalf("selected handles = %v want [command-2]", selected)
	}
	var result WaitToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Status != "process_done" {
		t.Fatalf("status = %q want process_done", result.Status)
	}
}

func TestWaitResumePreservesExactProcessHandles(t *testing.T) {
	loop := NewLoopEngine()
	loop.SetDeps(processCycleLoopDeps(true))
	deadline := time.Now().UTC().Add(10 * time.Minute)
	loop.Waits.EnterSleep(
		context.Background(), "s1", deadline, "waiting for command",
		[]WaitTrigger{WaitTriggerTimer, WaitTriggerProcessDone}, []string{"command-3"}, SleepMoverHost,
	)
	loop.Waits.breakSleep(t.Context(), "s1", "process.finished", true)
	reg := tools.NewDefaultRegistry()
	if err := RegisterWaitTool(reg, loop.Subscriptions, WaitToolDeps{}); err != nil {
		t.Fatalf("RegisterWaitTool: %v", err)
	}
	out, err := reg.Run(context.Background(), "wait", map[string]any{
		"resume": true,
	}, tools.ToolContext{SessionID: "s1", Agent: orchestration.ProfileCoordinator})
	if err != nil {
		t.Fatalf("wait resume exact process handle: %v", err)
	}
	var result WaitToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Conditions) != 1 || len(result.Conditions[0].Handles) != 1 || result.Conditions[0].Handles[0] != "command-3" {
		t.Fatalf("resumed conditions = %v want process_done [command-3]", result.Conditions)
	}
}

func TestWaitRejectsHandlesOutsideProcessDoneCondition(t *testing.T) {
	loop := NewLoopEngine()
	loop.SetDeps(idleWaitLoopDeps())
	reg := tools.NewDefaultRegistry()
	if err := RegisterWaitTool(reg, loop.Subscriptions, WaitToolDeps{}); err != nil {
		t.Fatalf("RegisterWaitTool: %v", err)
	}
	_, err := resolveConditions([]any{map[string]any{"kind": "scan_done", "handles": []any{"command-1"}}})
	if err == nil {
		t.Fatal("scan_done condition accepted process handles")
	}
}
